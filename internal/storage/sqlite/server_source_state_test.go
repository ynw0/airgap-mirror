package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestSourcePathCannotChangeAfterSync(t *testing.T) {
	ctx := context.Background()
	store := openServerDB(t)
	src := testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetState(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	state.LiveCursor = domain.Cursor{Kind: "apt-release", Value: "snapshot-1"}
	if err := store.PutState(ctx, state); err != nil {
		t.Fatal(err)
	}
	oldPath := src.RootPath
	src.RootPath = t.TempDir()
	if err := store.UpdateSource(ctx, src); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changing synced root must conflict: %v", err)
	}
	src.RootPath = oldPath
	src.Name = "renamed"
	if err := store.UpdateSource(ctx, src); err != nil {
		t.Fatalf("renaming a source should still work: %v", err)
	}
}

func TestSourcePathCanChangeBeforeFirstSync(t *testing.T) {
	ctx := context.Background()
	store := openServerDB(t)
	src := testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	src.RootPath = t.TempDir()
	if err := store.UpdateSource(ctx, src); err != nil {
		t.Fatalf("unsynced source path should be editable: %v", err)
	}
}

func TestSourceSyncConfigCannotChangeAfterSync(t *testing.T) {
	ctx := context.Background()
	store := openServerDB(t)
	src := testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetState(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	state.LiveCursor = domain.Cursor{Kind: "apt-release", Value: "snapshot-1"}
	if err = store.PutState(ctx, state); err != nil {
		t.Fatal(err)
	}
	src.ConfigJSON = []byte(`{"suite":"trixie","releaseMode":"inrelease"}`)
	if err = store.UpdateSource(ctx, src); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changing suite on a synced source should conflict: %v", err)
	}
	src.ConfigJSON = []byte(`{"suite":"bookworm","releaseMode":"inrelease","gcOwnsPool":true}`)
	if err = store.UpdateSource(ctx, src); err != nil {
		t.Fatalf("changing only the pool cleanup policy should be allowed: %v", err)
	}
}

func TestAPTTrustRotationPreservesCatalogAndRequiresIdleSource(t *testing.T) {
	ctx := context.Background()
	store := openServerDB(t)
	src := testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetState(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	state.LiveCursor = domain.Cursor{Kind: "apt-release", Value: "snapshot-1"}
	state.CatalogVersion = 3
	if err = store.PutState(ctx, state); err != nil {
		t.Fatal(err)
	}
	src.ConfigJSON = []byte(`{"suite":"bookworm","releaseMode":"inrelease","trustedKeyringBase64":"public-key-fixture","requireValidUntil":true}`)
	if err = store.UpdateSource(ctx, src); err != nil {
		t.Fatalf("idle key rotation should be allowed: %v", err)
	}
	after, err := store.GetState(ctx, src.ID)
	if err != nil || !after.LiveCursor.Equal(state.LiveCursor) || after.CatalogVersion != state.CatalogVersion {
		t.Fatalf("trust rotation changed live state: %+v %v", after, err)
	}
	state.ActiveEpochID = "active-epoch"
	if err = store.PutState(ctx, state); err != nil {
		t.Fatal(err)
	}
	src.ConfigJSON = []byte(`{"suite":"bookworm","releaseMode":"inrelease","trustedKeyringBase64":"another-fixture","requireValidUntil":true}`)
	if err = store.UpdateSource(ctx, src); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changed keys during active sync: %v", err)
	}
	state.ActiveEpochID = ""
	if err = store.PutState(ctx, state); err != nil {
		t.Fatal(err)
	}
	job := domain.MaintenanceJob{ID: mustID(t), SourceID: src.ID, Kind: domain.MaintenanceInventory, Status: domain.MaintenanceQueued, CreatedAt: time.Now()}
	if err = store.CreateMaintenanceJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err = store.UpdateSource(ctx, src); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changed keys during maintenance: %v", err)
	}
}

func TestMaintenanceReservationRejectsStaleSourceConfiguration(t *testing.T) {
	ctx := context.Background()
	store := openServerDB(t)
	src := testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.GetSource(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the timestamp unchanged to prove that comparing only a source
	// revision timestamp would not catch this change.
	src.RootPath = t.TempDir()
	if err = store.UpdateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	job := domain.MaintenanceJob{ID: mustID(t), SourceID: src.ID, Kind: domain.MaintenanceInventory, Status: domain.MaintenanceQueued, CreatedAt: time.Now()}
	if err = store.CreateMaintenanceJobForSource(ctx, job, snapshot); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale source reservation must conflict: %v", err)
	}
	if _, err = store.GetMaintenanceJob(ctx, job.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("failed reservation left a job: %v", err)
	}
	fresh, err := store.GetSource(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateMaintenanceJobForSource(ctx, job, fresh); err != nil {
		t.Fatalf("fresh reservation failed: %v", err)
	}
	fresh.RootPath = t.TempDir()
	if err = store.UpdateSource(ctx, fresh); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("reserved source must reject a repository path change: %v", err)
	}
}
