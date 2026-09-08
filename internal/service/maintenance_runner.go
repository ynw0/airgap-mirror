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
	root     context.Context
	Sources  ports.ServerStore
	Jobs     ports.MaintenanceStore
	Catalog  ports.CatalogStore
	Registry ports.AdapterRegistry
	Factory  ports.CatalogBuildFactory
	Now      func() time.Time

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func NewMaintenanceRunner(root context.Context, sources ports.ServerStore, jobs ports.MaintenanceStore, catalog ports.CatalogStore, registry ports.AdapterRegistry, factory ports.CatalogBuildFactory) *MaintenanceRunner {
	if root == nil {
		root = context.Background()
	}
	return &MaintenanceRunner{root: root, Sources: sources, Jobs: jobs, Catalog: catalog, Registry: registry, Factory: factory, cancels: map[string]context.CancelFunc{}}
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

func (r *MaintenanceRunner) Cancel(ctx context.Context, id string) (domain.MaintenanceJob, error) {
	job, err := r.Jobs.GetMaintenanceJob(ctx, id)
	if err != nil {
		return job, err
	}
	if job.Status != domain.MaintenanceQueued && job.Status != domain.MaintenanceRunning {
		return job, fmt.Errorf("maintenance job %s is already %s: %w", id, job.Status, domain.ErrConflict)
	}
	r.mu.Lock()
	cancel := r.cancels[id]
	r.mu.Unlock()
	if cancel == nil {
		return job, fmt.Errorf("maintenance job %s has no active worker: %w", id, domain.ErrConflict)
	}
	cancel()
	return job, nil
}

func (r *MaintenanceRunner) runInventory(ctx context.Context, job domain.MaintenanceJob, source domain.Source, inventory ports.InventoryAdapter) {
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
	sink, err := r.Factory.Create(ctx, job.ID, source.ID)
	if err != nil {
		r.finishFailure(job.ID, err)
		return
	}
	defer sink.Abort()
	if err = r.persist(func(pctx context.Context) error { return r.Jobs.SetMaintenanceCatalogPath(pctx, job.ID, sink.Path()) }); err != nil {
		r.finishFailure(job.ID, err)
		return
	}

	lastPersist := time.Time{}
	lastProgress := ports.InventoryProgress{}
	report := func(p ports.InventoryProgress) error {
		if p.ScannedObjects < lastProgress.ScannedObjects || p.ScannedBytes < lastProgress.ScannedBytes {
			return fmt.Errorf("inventory progress moved backwards: %w", domain.ErrConflict)
		}
		lastProgress = p
		now := r.now()
		if !lastPersist.IsZero() && now.Sub(lastPersist) < time.Second {
			return ctx.Err()
		}
		if err := r.persist(func(pctx context.Context) error {
			return r.Jobs.UpdateMaintenanceProgress(pctx, job.ID, ports.MaintenanceProgress{ScannedObjects: p.ScannedObjects, ScannedBytes: p.ScannedBytes})
		}); err != nil {
			return err
		}
		lastPersist = now
		return ctx.Err()
	}

	reported, err := inventory.Inventory(ctx, ports.InventoryRequest{Source: source, Current: r.Catalog, Report: report}, sink)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			r.finishCancelled(job.ID)
			return
		}
		r.finishFailure(job.ID, err)
		return
	}
	actual, err := sink.Stats(ctx)
	if err != nil {
		r.finishFailure(job.ID, err)
		return
	}
	if actual.SourceID != source.ID || reported.SourceID != source.ID || actual.Objects != reported.Objects || actual.Bytes != reported.Bytes {
		r.finishFailure(job.ID, fmt.Errorf("inventory scanner/sink stats mismatch reported=%s:%d/%d actual=%s:%d/%d: %w", reported.SourceID, reported.Objects, reported.Bytes, actual.SourceID, actual.Objects, actual.Bytes, domain.ErrConflict))
		return
	}
	if err = r.persist(func(pctx context.Context) error {
		return r.Jobs.UpdateMaintenanceProgress(pctx, job.ID, ports.MaintenanceProgress{ScannedObjects: actual.Objects, ScannedBytes: actual.Bytes})
	}); err != nil {
		r.finishFailure(job.ID, err)
		return
	}
	if err = sink.Close(); err != nil {
		r.finishFailure(job.ID, err)
		return
	}
	if err = r.persist(func(pctx context.Context) error { return r.Jobs.ReplaceCatalogFromFile(pctx, source.ID, sink.Path(), actual, r.now()) }); err != nil {
		r.finishFailure(job.ID, err)
		return
	}
	_ = r.transition(job.ID, domain.MaintenanceCompleted, "")
}

func (r *MaintenanceRunner) finishFailure(id string, cause error) {
	_ = r.transition(id, domain.MaintenanceFailed, cause.Error())
}

func (r *MaintenanceRunner) finishCancelled(id string) {
	_ = r.transition(id, domain.MaintenanceCancelled, "cancelled by operator")
}

func (r *MaintenanceRunner) transition(id string, status domain.MaintenanceStatus, message string) error {
	return r.persist(func(ctx context.Context) error { return r.Jobs.TransitionMaintenanceJob(ctx, id, status, message, r.now()) })
}

func (r *MaintenanceRunner) persist(fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return fn(ctx)
}
