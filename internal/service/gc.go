package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

func (r *MaintenanceRunner) StartGC(ctx context.Context, sourceID string, execute bool) (domain.MaintenanceJob, error) {
	var empty domain.MaintenanceJob
	if sourceID == "" || r.Sources == nil || r.Jobs == nil || r.Catalog == nil || r.Registry == nil || r.Candidates == nil {
		return empty, fmt.Errorf("GC runner is not fully configured: %w", domain.ErrInvalid)
	}
	if _, ok := r.Catalog.(ports.CatalogMembership); !ok {
		return empty, fmt.Errorf("catalog store does not support membership queries: %w", domain.ErrInvalid)
	}
	source, err := r.Sources.GetSource(ctx, sourceID)
	if err != nil {
		return empty, err
	}
	if _, err = r.requireIdleSource(ctx, source.ID); err != nil {
		return empty, err
	}
	if _, err = r.exclusiveRoot(ctx, source); err != nil {
		return empty, err
	}
	adapter, err := r.Registry.Get(source.Type, source.Provider)
	if err != nil {
		return empty, err
	}
	managedAdapter, ok := adapter.(ports.ManagedPathAdapter)
	if !ok {
		return empty, fmt.Errorf("adapter %s/%s does not support GC path ownership: %w", source.Type, source.Provider, domain.ErrInvalid)
	}
	matcher, err := managedAdapter.ManagedPaths(ctx, source)
	if err != nil {
		return empty, err
	}
	if matcher == nil {
		return empty, fmt.Errorf("adapter %s/%s has physical GC disabled by source configuration: %w", source.Type, source.Provider, domain.ErrInvalid)
	}
	id, err := domain.NewID()
	if err != nil {
		return empty, err
	}
	job := domain.MaintenanceJob{ID: id, SourceID: sourceID, Kind: domain.MaintenanceGC, Status: domain.MaintenanceQueued, Execute: execute, CreatedAt: r.now()}
	if err = r.Jobs.CreateMaintenanceJob(ctx, job); err != nil {
		return empty, err
	}
	runCtx, cancel := context.WithCancel(r.root)
	r.mu.Lock()
	r.cancels[id] = cancel
	r.mu.Unlock()
	go r.runGC(runCtx, job, source, matcher)
	return job, nil
}

func (r *MaintenanceRunner) ListGCCandidates(ctx context.Context, jobID, after string, limit int) ([]domain.GCCandidate, domain.GCStats, error) {
	var stats domain.GCStats
	job, err := r.Jobs.GetMaintenanceJob(ctx, jobID)
	if err != nil {
		return nil, stats, err
	}
	if job.Kind != domain.MaintenanceGC || job.TempCatalogPath == "" || r.Candidates == nil {
		return nil, stats, fmt.Errorf("maintenance job %s has no GC candidate database: %w", jobID, domain.ErrInvalid)
	}
	reader, err := r.Candidates.Open(ctx, job.TempCatalogPath)
	if err != nil {
		return nil, stats, err
	}
	defer reader.Close()
	stats, err = reader.Stats(ctx)
	if err != nil {
		return nil, stats, err
	}
	items, err := reader.List(ctx, after, limit)
	return items, stats, err
}

func (r *MaintenanceRunner) requireIdleSource(ctx context.Context, sourceID string) (domain.SourceState, error) {
	state, err := r.Sources.GetState(ctx, sourceID)
	if err != nil {
		return state, err
	}
	if state.ActiveEpochID != "" {
		return state, fmt.Errorf("source %s has active epoch %s: %w", sourceID, state.ActiveEpochID, domain.ErrConflict)
	}
	return state, nil
}

func canonicalRepositoryRoot(raw string) (string, string, error) {
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", "", err
	}
	abs = filepath.Clean(abs)
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("repository root %s is not a directory: %w", raw, domain.ErrInvalid)
	}
	return abs, filepath.Clean(real), nil
}

func rootsOverlap(a, b string) bool {
	rel, err := filepath.Rel(a, b)
	if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
		return true
	}
	rel, err = filepath.Rel(b, a)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

func (r *MaintenanceRunner) exclusiveRoot(ctx context.Context, source domain.Source) (string, error) {
	currentAbs, currentReal, err := canonicalRepositoryRoot(source.RootPath)
	if err != nil {
		return "", err
	}
	all, err := r.Sources.ListSources(ctx)
	if err != nil {
		return "", err
	}
	for _, other := range all {
		if other.ID == source.ID {
			continue
		}
		otherAbs, absErr := filepath.Abs(other.RootPath)
		if absErr != nil {
			return "", absErr
		}
		otherAbs = filepath.Clean(otherAbs)
		if rootsOverlap(currentAbs, otherAbs) {
			return "", fmt.Errorf("source %s repository root overlaps source %s; physical GC is refused: %w", source.ID, other.ID, domain.ErrConflict)
		}
		otherReal, evalErr := filepath.EvalSymlinks(otherAbs)
		if evalErr == nil && rootsOverlap(currentReal, filepath.Clean(otherReal)) {
			return "", fmt.Errorf("source %s resolved repository root overlaps source %s; physical GC is refused: %w", source.ID, other.ID, domain.ErrConflict)
		}
	}
	return currentReal, nil
}

func (r *MaintenanceRunner) runGC(ctx context.Context, job domain.MaintenanceJob, source domain.Source, matcher ports.ManagedPathMatcher) {
	defer func() {
		r.mu.Lock()
		if cancel := r.cancels[job.ID]; cancel != nil {
			cancel()
		}
		delete(r.cancels, job.ID)
		r.mu.Unlock()
	}()
	if err := r.transition(job.ID, domain.MaintenanceRunning, ""); err != nil {
		return
	}
	membership := r.Catalog.(ports.CatalogMembership)
	root, err := r.exclusiveRoot(ctx, source)
	if err != nil {
		r.finishFailure(job.ID, err)
		return
	}
	writer, err := r.Candidates.Create(ctx, job.ID)
	if err != nil {
		r.finishFailure(job.ID, err)
		return
	}
	keepPlan := false
	defer func() {
		if !keepPlan {
			_ = writer.Abort()
		}
	}()
	if err = r.persist(func(pctx context.Context) error { return r.Jobs.SetMaintenanceCatalogPath(pctx, job.ID, writer.Path()) }); err != nil {
		r.finishFailure(job.ID, err)
		return
	}

	var scannedObjects, scannedBytes, candidateObjects, candidateBytes int64
	lastPersist := time.Time{}
	persistProgress := func(force bool) error {
		now := r.now()
		if !force && !lastPersist.IsZero() && now.Sub(lastPersist) < time.Second {
			return nil
		}
		err := r.persist(func(pctx context.Context) error {
			return r.Jobs.UpdateMaintenanceProgress(pctx, job.ID, ports.MaintenanceProgress{ScannedObjects: scannedObjects, ScannedBytes: scannedBytes, CandidateObjects: candidateObjects, CandidateBytes: candidateBytes})
		})
		if err == nil {
			lastPersist = now
		}
		return err
	}

	err = filepath.WalkDir(root, func(filePath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if filePath == root {
			return nil
		}
		rel, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		logical := filepath.ToSlash(rel)
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("GC refuses repository symlink %s; remove or materialize it before physical GC: %w", logical, domain.ErrConflict)
		}
		if d.IsDir() {
			return nil
		}
		owned, err := matcher(logical)
		if err != nil {
			return err
		}
		if !owned {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("managed GC path %s is not a regular file: %w", logical, domain.ErrConflict)
		}
		scannedObjects++
		scannedBytes += info.Size()
		contains, err := membership.Contains(ctx, source.ID, logical)
		if err != nil {
			return err
		}
		if !contains {
			candidate := domain.GCCandidate{LogicalPath: logical, Size: info.Size(), ModTimeUnixNano: info.ModTime().UnixNano()}
			if err = writer.Put(ctx, candidate); err != nil {
				return err
			}
			candidateObjects++
			candidateBytes += info.Size()
		}
		if scannedObjects%4096 == 0 {
			return persistProgress(false)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			r.finishCancelled(job.ID)
			return
		}
		r.finishFailure(job.ID, err)
		return
	}
	if err = persistProgress(true); err != nil {
		r.finishFailure(job.ID, err)
		return
	}
	planned, err := writer.Stats(ctx)
	if err != nil {
		r.finishFailure(job.ID, err)
		return
	}
	if planned.Objects != candidateObjects || planned.Bytes != candidateBytes {
		r.finishFailure(job.ID, fmt.Errorf("GC candidate stats mismatch planned=%d/%d database=%d/%d: %w", candidateObjects, candidateBytes, planned.Objects, planned.Bytes, domain.ErrConflict))
		return
	}
	if err = writer.Close(); err != nil {
		r.finishFailure(job.ID, err)
		return
	}
	keepPlan = true
	if !job.Execute {
		_ = r.transition(job.ID, domain.MaintenanceCompleted, "")
		return
	}

	if _, err = r.requireIdleSource(ctx, source.ID); err != nil {
		r.finishFailure(job.ID, err)
		return
	}
	executeRoot, err := r.exclusiveRoot(ctx, source)
	if err != nil || executeRoot != root {
		if err == nil {
			err = fmt.Errorf("repository root changed between GC planning and execution: %w", domain.ErrConflict)
		}
		r.finishFailure(job.ID, err)
		return
	}
	reader, err := r.Candidates.Open(ctx, writer.Path())
	if err != nil {
		r.finishFailure(job.ID, err)
		return
	}
	defer reader.Close()
	var affectedObjects, affectedBytes int64
	err = reader.Walk(ctx, func(candidate domain.GCCandidate) error {
		contains, err := membership.Contains(ctx, source.ID, candidate.LogicalPath)
		if err != nil {
			return err
		}
		if contains {
			return fmt.Errorf("GC candidate %s became referenced after planning: %w", candidate.LogicalPath, domain.ErrConflict)
		}
		if err = secureRemoveCandidate(root, candidate.LogicalPath, candidate); err != nil {
			return err
		}
		affectedObjects++
		affectedBytes += candidate.Size
		if affectedObjects%1024 == 0 {
			return r.persist(func(pctx context.Context) error {
				return r.Jobs.UpdateMaintenanceProgress(pctx, job.ID, ports.MaintenanceProgress{ScannedObjects: scannedObjects, ScannedBytes: scannedBytes, CandidateObjects: candidateObjects, CandidateBytes: candidateBytes, AffectedObjects: affectedObjects, AffectedBytes: affectedBytes})
			})
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			r.finishCancelled(job.ID)
			return
		}
		r.finishFailure(job.ID, err)
		return
	}
	if err = r.persist(func(pctx context.Context) error {
		return r.Jobs.UpdateMaintenanceProgress(pctx, job.ID, ports.MaintenanceProgress{ScannedObjects: scannedObjects, ScannedBytes: scannedBytes, CandidateObjects: candidateObjects, CandidateBytes: candidateBytes, AffectedObjects: affectedObjects, AffectedBytes: affectedBytes})
	}); err != nil {
		r.finishFailure(job.ID, err)
		return
	}
	_ = r.transition(job.ID, domain.MaintenanceCompleted, "")
}
