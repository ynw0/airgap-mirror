package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestCompleteNoopEpochUnblocksNextAnalysis(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "client.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitClient(ctx, db); err != nil {
		t.Fatal(err)
	}

	store := NewClientStore(db)
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `INSERT INTO state_capsules(id,file_path,exported_at,imported_at,state_json) VALUES(?,?,?,?,?)`,
		"capsule", "", timeString(now), timeString(now), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	cursor := domain.Cursor{Kind: "test", Value: "42"}
	epoch := domain.Epoch{
		ID: "epoch", SourceID: "source", BaseCursor: cursor, TargetCursor: cursor,
		Status: domain.EpochPlanned, CreatedAt: now,
	}
	if err := store.Begin(ctx, epoch, "capsule"); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, domain.EpochPlan{Epoch: epoch}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindOpenEpoch(ctx, "capsule", "source"); err != nil {
		t.Fatalf("planned epoch should be open: %v", err)
	}
	if err := store.CompleteNoopEpoch(ctx, epoch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindOpenEpoch(ctx, "capsule", "source"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("completed no-op epoch must not block next analysis: %v", err)
	}
	got, err := store.GetEpoch(ctx, epoch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.EpochComplete {
		t.Fatalf("status = %s, want COMPLETE", got.Status)
	}
}

func TestCompleteNoopEpochRejectsWork(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "client.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitClient(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `INSERT INTO state_capsules(id,file_path,exported_at,imported_at,state_json) VALUES(?,?,?,?,?)`,
		"capsule", "", timeString(now), timeString(now), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	store := NewClientStore(db)
	epoch := domain.Epoch{ID: "epoch", SourceID: "source",
		BaseCursor:   domain.Cursor{Kind: "test", Value: "1"},
		TargetCursor: domain.Cursor{Kind: "test", Value: "2"},
		Status:       domain.EpochPlanned, CreatedAt: now}
	if err := store.Begin(ctx, epoch, "capsule"); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, domain.EpochPlan{Epoch: epoch}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteNoopEpoch(ctx, epoch.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cursor-changing epoch must not be closed as no-op: %v", err)
	}
}
