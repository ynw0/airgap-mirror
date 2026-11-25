package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ynw0/airgap-mirror/internal/adapters/pypi"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
	storesqlite "github.com/ynw0/airgap-mirror/internal/storage/sqlite"
)

type checkpointTestAdapter struct {
	*pypi.Adapter
	reached chan struct{}
}

func (a *checkpointTestAdapter) Inventory(ctx context.Context, req ports.InventoryRequest, sink ports.CatalogBuildSink) (domain.CatalogStats, error) {
	out := domain.CatalogStats{SourceID: req.Source.ID}
	projectSink, ok := sink.(ports.ProjectInventorySink)
	if !ok {
		return out, fmt.Errorf("missing resumable checkpoint sink")
	}
	starting, err := projectSink.Restore(ctx, "test-source", "test-projects", 101)
	if err != nil {
		return out, err
	}
	if req.Report != nil {
		if err = req.Report(starting); err != nil {
			return out, err
		}
	}
	for index := starting.CompletedProjects; index < 101; index++ {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		project := fmt.Sprintf("pkg-%04d", index)
		entry := domain.CatalogEntry{
			SourceID: req.Source.ID, LogicalPath: "packages/" + project + ".whl",
			Size: 1, SHA256: strings.Repeat("a", 64), UpdatedAt: time.Now(),
		}
		if err = sink.Put(ctx, entry); err != nil {
			return out, err
		}
		progress, durable, checkpointErr := projectSink.CheckpointProject(ctx, project, 1, int64(index))
		if checkpointErr != nil {
			return out, checkpointErr
		}
		if durable && req.Report != nil {
			if err = req.Report(progress); err != nil {
				return out, err
			}
		}
		if index == 99 && starting.CompletedProjects == 0 {
			close(a.reached)
			<-ctx.Done()
			return out, ctx.Err()
		}
	}
	progress, err := projectSink.Flush(ctx)
	if err != nil {
		return out, err
	}
	if req.Report != nil {
		if err = req.Report(progress); err != nil {
			return out, err
		}
	}
	return sink.Stats(ctx)
}

type checkpointRegistry struct{ adapter ports.SourceAdapter }

func (r checkpointRegistry) Get(domain.SourceType, string) (ports.SourceAdapter, error) {
	return r.adapter, nil
}

func waitMaintenanceStatus(t *testing.T, store *storesqlite.ServerStore, id string, wanted domain.MaintenanceStatus) domain.MaintenanceJob {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.GetMaintenanceJob(context.Background(), id)
		if err == nil && job.Status == wanted {
			return job
		}
		time.Sleep(15 * time.Millisecond)
	}
	job, err := store.GetMaintenanceJob(context.Background(), id)
	t.Fatalf("job did not reach %s: %+v %v", wanted, job, err)
	return domain.MaintenanceJob{}
}

func TestRunnerPauseAndResumeKeepsLiveCatalogUntilComplete(t *testing.T) {
	t.Run("operator pause", func(t *testing.T) { testRunnerPauseAndResume(t, false) })
	t.Run("agent shutdown", func(t *testing.T) { testRunnerPauseAndResume(t, true) })
}

func testRunnerPauseAndResume(t *testing.T, shutdown bool) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/server.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	if err = storesqlite.InitServer(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := storesqlite.NewServerStore(db)
	id, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	source := domain.Source{ID: id, Name: "Large PyPI", Type: domain.SourcePyPI, Provider: pypi.Provider, UpstreamURL: "https://pypi.org/simple", PublicURL: "https://repo.example/pypi", RootPath: t.TempDir(), ConfigJSON: json.RawMessage("{}"), Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err = store.CreateSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err = store.Upsert(ctx, domain.CatalogEntry{SourceID: id, LogicalPath: "old/untouched", Size: 5, SHA256: strings.Repeat("b", 64), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	adapter := &checkpointTestAdapter{Adapter: pypi.New(nil), reached: make(chan struct{})}
	factory := storesqlite.NewRebuildCatalogFactory(t.TempDir())
	registry := checkpointRegistry{adapter: adapter}
	root, cancel := context.WithCancel(ctx)
	defer cancel()
	runner := NewMaintenanceRunner(root, store, store, store, registry, factory)
	job, err := runner.StartInventory(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-adapter.reached:
	case <-time.After(8 * time.Second):
		t.Fatal("first checkpoint not reached")
	}
	if shutdown {
		cancel()
	} else if _, err = runner.PauseInventory(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	paused := waitMaintenanceStatus(t, store, job.ID, domain.MaintenancePaused)
	if paused.CompletedProjects != 100 || paused.ScannedObjects != 100 {
		t.Fatalf("pause lost committed progress: %+v", paused)
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 8*time.Second)
	defer waitCancel()
	if err = runner.WaitForIdle(waitCtx); err != nil {
		t.Fatalf("paused worker failed to release checkpoint: %v", err)
	}
	if _, err = store.Get(ctx, id, "old/untouched"); err != nil {
		t.Fatalf("live catalog modified before completion: %v", err)
	}
	if shutdown {
		if _, err = runner.ResumeInventory(ctx, job.ID); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("stopping Agent accepted a resume: %v", err)
		}
	}
	// A new runner represents an Agent restart; it reopens the same job and SQLite checkpoint.
	restarted := NewMaintenanceRunner(ctx, store, store, store, registry, factory)
	if err = restarted.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.ResumeInventory(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	done := waitMaintenanceStatus(t, store, job.ID, domain.MaintenanceCompleted)
	finishCtx, finishCancel := context.WithTimeout(ctx, 8*time.Second)
	defer finishCancel()
	if err = restarted.WaitForIdle(finishCtx); err != nil {
		t.Fatal(err)
	}
	if done.CompletedProjects != 101 || done.ScannedObjects != 101 {
		t.Fatalf("completion stats not persisted: %+v", done)
	}
	if _, err = store.Get(ctx, id, "old/untouched"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old catalog still present: %v", err)
	}
	stat, err := store.Stats(ctx, id)
	if err != nil || stat.Objects != 101 {
		t.Fatalf("bad published catalog: %+v %v", stat, err)
	}
}
