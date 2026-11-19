package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

type maintenanceRecoveryStore interface {
	RecoverInterruptedMaintenanceJobs(context.Context, time.Time, string) error
}

type MaintenanceRunner struct {
	root       context.Context
	Sources    ports.ServerStore
	Jobs       ports.MaintenanceStore
	Catalog    ports.CatalogStore
	Registry   ports.AdapterRegistry
	Factory    ports.CatalogBuildFactory
	Candidates ports.GCCandidateFactory
	Now        func() time.Time

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	intents map[string]string
}

func NewMaintenanceRunner(root context.Context, sources ports.ServerStore, jobs ports.MaintenanceStore, catalog ports.CatalogStore, registry ports.AdapterRegistry, factory ports.CatalogBuildFactory) *MaintenanceRunner {
	if root == nil {
		root = context.Background()
	}
	return &MaintenanceRunner{root: root, Sources: sources, Jobs: jobs, Catalog: catalog, Registry: registry, Factory: factory, cancels: map[string]context.CancelFunc{}, intents: map[string]string{}}
}

func (r *MaintenanceRunner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *MaintenanceRunner) RecoverInterrupted(ctx context.Context) error {
	recovery, ok := r.Jobs.(maintenanceRecoveryStore)
	if !ok {
		return fmt.Errorf("maintenance store does not support interrupted-job recovery: %w", domain.ErrInvalid)
	}
	return recovery.RecoverInterruptedMaintenanceJobs(ctx, r.now(), "agent process restarted before maintenance completed")
}

func (r *MaintenanceRunner) StartInventory(ctx context.Context, sourceID string) (domain.MaintenanceJob, error) {
	var empty domain.MaintenanceJob
	if sourceID == "" || r.Sources == nil || r.Jobs == nil || r.Catalog == nil || r.Registry == nil || r.Factory == nil {
		return empty, fmt.Errorf("maintenance runner is not fully configured: %w", domain.ErrInvalid)
	}
	source, err := r.Sources.GetSource(ctx, sourceID)
	if err != nil {
		return empty, err
	}
	state, err := r.Sources.GetState(ctx, sourceID)
	if err != nil {
		return empty, err
	}
	if state.ActiveEpochID != "" {
		return empty, fmt.Errorf("source %s has active epoch %s: %w", sourceID, state.ActiveEpochID, domain.ErrConflict)
	}
	adapter, err := r.Registry.Get(source.Type, source.Provider)
	if err != nil {
		return empty, err
	}
	inventory, ok := adapter.(ports.InventoryAdapter)
	if !ok {
		return empty, fmt.Errorf("adapter %s/%s does not support inventory: %w", source.Type, source.Provider, domain.ErrInvalid)
	}
	id, err := domain.NewID()
	if err != nil {
		return empty, err
	}
	job := domain.MaintenanceJob{ID: id, SourceID: sourceID, Kind: domain.MaintenanceInventory, Status: domain.MaintenanceQueued, CreatedAt: r.now()}
	if err = r.Jobs.CreateMaintenanceJob(ctx, job); err != nil {
		return empty, err
	}
	runCtx, cancel := context.WithCancel(r.root)
	r.mu.Lock()
	r.cancels[id] = cancel
	r.mu.Unlock()
	go r.runInventory(runCtx, job, source, inventory)
	return job, nil
}

// ResumeInventory continues the same job and its durable SQLite checkpoint.
// A paused job reserves the source: no new Epoch may start until it completes or is discarded.
func (r *MaintenanceRunner) ResumeInventory(ctx context.Context, id string) (domain.MaintenanceJob, error) {
	job, err := r.Jobs.GetMaintenanceJob(ctx, id)
	if err != nil { return job, err }
	if job.Kind != domain.MaintenanceInventory || job.Status != domain.MaintenancePaused {
		return job, fmt.Errorf("只有已暂停的文件清点任务可以继续: %w", domain.ErrConflict)
	}
	source, err := r.Sources.GetSource(ctx, job.SourceID)
	if err != nil { return job, err }
	if source.Type != domain.SourcePyPI || source.Provider != "pypi" {
		return job, fmt.Errorf("断点续检仅支持 PyPI: %w", domain.ErrInvalid)
	}
	state, err := r.Sources.GetState(ctx, job.SourceID)
	if err != nil { return job, err }
	if state.ActiveEpochID != "" {
		return job, fmt.Errorf("软件源仍有同步任务: %w", domain.ErrConflict)
	}
	adapter, err := r.Registry.Get(source.Type, source.Provider)
	if err != nil { return job, err }
	inventory, ok := adapter.(ports.InventoryAdapter)
	if !ok { return job, domain.ErrInvalid }
	r.mu.Lock()
	if r.cancels[id] != nil {
		r.mu.Unlock()
		return job, fmt.Errorf("清点任务还在退出，不能重复继续: %w", domain.ErrConflict)
	}
	runCtx, cancel := context.WithCancel(r.root)
	r.cancels[id] = cancel
	r.mu.Unlock()
	go r.runInventory(runCtx, job, source, inventory)
	return job, nil
}

func (r *MaintenanceRunner) PauseInventory(ctx context.Context, id string) (domain.MaintenanceJob, error) {
	job, err := r.Jobs.GetMaintenanceJob(ctx, id)
	if err != nil { return job, err }
	if job.Kind != domain.MaintenanceInventory || (job.Status != domain.MaintenanceRunning && job.Status != domain.MaintenanceQueued) {
		return job, fmt.Errorf("只有正在扫描的任务可以暂停: %w", domain.ErrConflict)
	}
	source, err := r.Sources.GetSource(ctx, job.SourceID)
	if err != nil { return job, err }
	if source.Type != domain.SourcePyPI || source.Provider != "pypi" {
		return job, fmt.Errorf("断点暂停仅支持 PyPI: %w", domain.ErrInvalid)
	}
	r.mu.Lock()
	cancel := r.cancels[id]
	if cancel != nil { r.intents[id] = "pause" }
	r.mu.Unlock()
	if cancel == nil { return job, fmt.Errorf("任务工作线程已停止，请刷新: %w", domain.ErrConflict) }
	cancel()
	return job, nil
}

func (r *MaintenanceRunner) Cancel(ctx context.Context, id string) (domain.MaintenanceJob, error) {
	job, err := r.Jobs.GetMaintenanceJob(ctx, id)
	if err != nil {
		return job, err
	}
	if job.Status == domain.MaintenancePaused {
		if err = r.transition(id, domain.MaintenanceCancelled, "cancelled by operator"); err != nil { return job, err }
		if factory, ok := r.Factory.(ports.ProjectInventoryFactory); ok {
			_ = factory.DiscardProjectInventory(id)
		}
		return r.Jobs.GetMaintenanceJob(ctx, id)
	}
	if job.Status != domain.MaintenanceQueued && job.Status != domain.MaintenanceRunning {
		return job, fmt.Errorf("maintenance job %s is already %s: %w", id, job.Status, domain.ErrConflict)
	}
	r.mu.Lock()
	cancel := r.cancels[id]
	if cancel != nil { r.intents[id] = "cancel" }
	r.mu.Unlock()
	if cancel == nil {
		return job, fmt.Errorf("maintenance job %s has no active worker: %w", id, domain.ErrConflict)
	}
	cancel()
	return job, nil
}

func (r *MaintenanceRunner) runInventory(ctx context.Context, job domain.MaintenanceJob, source domain.Source, inventory ports.InventoryAdapter) {
	resumable := source.Type == domain.SourcePyPI && source.Provider == "pypi"
	var projectSink ports.ProjectInventorySink
	defer func() {
		r.mu.Lock()
		if cancel := r.cancels[job.ID]; cancel != nil { cancel() }
		delete(r.cancels, job.ID)
		delete(r.intents, job.ID)
		r.mu.Unlock()
	}()

	if err := r.transition(job.ID, domain.MaintenanceRunning, ""); err != nil { return }
	var sink ports.CatalogBuildSink
	var err error
	if resumable {
		factory, ok := r.Factory.(ports.ProjectInventoryFactory)
		if !ok {
			r.finishFailure(job.ID, fmt.Errorf("PyPI checkpoint factory not available: %w", domain.ErrInvalid))
			return
		}
		projectSink, err = factory.OpenProjectInventory(ctx, job.ID, source.ID)
		sink = projectSink
	} else {
		sink, err = r.Factory.Create(ctx, job.ID, source.ID)
	}
	if err != nil {
		r.finishInventoryError(job.ID, resumable, err)
		return
	}
	defer sink.Abort()
	if err = r.persist(func(pctx context.Context) error { return r.Jobs.SetMaintenanceCatalogPath(pctx, job.ID, sink.Path()) }); err != nil {
		r.finishInventoryError(job.ID, resumable, err)
		return
	}

	lastPersist := time.Time{}
	lastProgress := ports.InventoryProgress{}
	report := func(p ports.InventoryProgress) error {
		if p.ScannedObjects < lastProgress.ScannedObjects || p.ScannedBytes < lastProgress.ScannedBytes ||
			p.CompletedProjects < lastProgress.CompletedProjects {
			return fmt.Errorf("inventory progress moved backwards: %w", domain.ErrConflict)
		}
		lastProgress = p
		now := r.now()
		if !resumable && !lastPersist.IsZero() && now.Sub(lastPersist) < time.Second {
			return ctx.Err()
		}
		if err := r.persist(func(pctx context.Context) error {
			progress := ports.MaintenanceProgress{
				ScannedObjects: p.ScannedObjects, ScannedBytes: p.ScannedBytes,
				CompletedProjects: p.CompletedProjects, TotalProjects: p.TotalProjects, LastProject: p.LastProject,
			}
			if resumable { return r.Jobs.UpdateInventoryCheckpoint(pctx, job.ID, progress) }
			return r.Jobs.UpdateMaintenanceProgress(pctx, job.ID, progress)
		}); err != nil { return err }
		lastPersist = now
		return ctx.Err()
	}

	reported, err := inventory.Inventory(ctx, ports.InventoryRequest{Source: source, Current: r.Catalog, Report: report}, sink)
	if err != nil {
		r.finishInventoryError(job.ID, resumable, err)
		return
	}
	actual, err := sink.Stats(ctx)
	if err != nil { r.finishInventoryError(job.ID, resumable, err); return }
	if actual.SourceID != source.ID || reported.SourceID != source.ID || actual.Objects != reported.Objects || actual.Bytes != reported.Bytes {
		r.finishInventoryError(job.ID, resumable, fmt.Errorf("inventory scanner/sink stats mismatch: %w", domain.ErrConflict))
		return
	}
	if !resumable {
		if err = r.persist(func(pctx context.Context) error {
			return r.Jobs.UpdateMaintenanceProgress(pctx, job.ID, ports.MaintenanceProgress{ScannedObjects: actual.Objects, ScannedBytes: actual.Bytes})
		}); err != nil {
			r.finishInventoryError(job.ID, resumable, err)
			return
		}
	}
	if err = sink.Close(); err != nil {
		r.finishInventoryError(job.ID, resumable, err)
		return
	}
	if err = r.persist(func(pctx context.Context) error {
		return r.Jobs.ReplaceCatalogFromFile(pctx, source.ID, sink.Path(), actual, r.now())
	}); err != nil {
		r.finishInventoryError(job.ID, resumable, err)
		return
	}
	_ = r.transition(job.ID, domain.MaintenanceCompleted, "")
	if resumable {
		if factory, ok := r.Factory.(ports.ProjectInventoryFactory); ok {
			_ = factory.DiscardProjectInventory(job.ID)
		}
	}
}

func (r *MaintenanceRunner) finishInventoryError(id string, resumable bool, cause error) {
	r.mu.Lock()
	intent := r.intents[id]
	r.mu.Unlock()
	if intent == "cancel" {
		r.finishCancelled(id)
		if resumable {
			if factory, ok := r.Factory.(ports.ProjectInventoryFactory); ok { _ = factory.DiscardProjectInventory(id) }
		}
		return
	}
	if resumable {
		reason := cause.Error()
		if errors.Is(cause, context.Canceled) { reason = "已暂停，已完成的检查点保留，可点击继续" }
		_ = r.transition(id, domain.MaintenancePaused, reason)
		return
	}
	if errors.Is(cause, context.Canceled) {
		r.finishCancelled(id)
		return
	}
	r.finishFailure(id, cause)
}

func (r *MaintenanceRunner) finishFailure(id string, cause error) {
	_ = r.transition(id, domain.MaintenanceFailed, cause.Error())
}

func (r *MaintenanceRunner) finishCancelled(id string) {
	_ = r.transition(id, domain.MaintenanceCancelled, "cancelled by operator")
}

func (r *MaintenanceRunner) transition(id string, status domain.MaintenanceStatus, message string) error {
	return r.persist(func(ctx context.Context) error {
		return r.Jobs.TransitionMaintenanceJob(ctx, id, status, message, r.now())
	})
}

func (r *MaintenanceRunner) persist(fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return fn(ctx)
}
