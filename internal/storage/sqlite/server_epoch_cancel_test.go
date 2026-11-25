package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestCancelEpochClearsActiveAndFreesSource(t *testing.T) {
	ctx := context.Background()
	store := openServerDB(t)
	src := testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	eid, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = store.CreateEpoch(ctx, domain.Epoch{ID: eid, SourceID: src.ID, Status: domain.EpochTransferring, TotalBatches: 1, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetState(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	state.ActiveEpochID = eid
	state.UpdatedAt = now
	if err = store.PutState(ctx, state); err != nil {
		t.Fatal(err)
	}
	eid2, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateEpoch(ctx, domain.Epoch{ID: eid2, SourceID: src.ID, Status: domain.EpochTransferring, TotalBatches: 1, CreatedAt: now}); err == nil {
		t.Fatal("second non-terminal epoch should be rejected")
	}
	if err = store.CancelEpoch(ctx, eid, now, "epoch cancelled"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetEpoch(ctx, eid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.EpochCancelled {
		t.Fatalf("status=%s", got.Status)
	}
	state, err = store.GetState(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveEpochID != "" {
		t.Fatalf("active epoch still %s", state.ActiveEpochID)
	}
	if err = store.CancelEpoch(ctx, eid, now, "epoch cancelled"); err != nil {
		t.Fatal(err)
	}
	if err = store.CreateEpoch(ctx, domain.Epoch{ID: eid2, SourceID: src.ID, Status: domain.EpochTransferring, TotalBatches: 1, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func TestCancelEpochRefusesImportedData(t *testing.T) {
	ctx := context.Background()
	store := openServerDB(t)
	src := testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	eid, _ := domain.NewID()
	uid, _ := domain.NewID()
	entry, _ := domain.NewID()
	now := time.Now().UTC()
	if err := store.CreateEpoch(ctx, domain.Epoch{ID: eid, SourceID: src.ID, Status: domain.EpochTransferring, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetState(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	state.ActiveEpochID = eid
	state.UpdatedAt = now
	if err := store.PutState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO imported_entries(epoch_id,entry_id,source_id,logical_path,sha256,publish_unit_id,metadata,imported_at) VALUES(?,?,?,?,?,?,0,?)`, eid, entry, src.ID, "pool/a.deb", "sha", uid, timeString(now)); err != nil {
		t.Fatal(err)
	}
	if err := store.CancelEpoch(ctx, eid, now, "cancel"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cancelling imported epoch should conflict: %v", err)
	}
	state, err = store.GetState(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveEpochID != eid {
		t.Fatalf("epoch lock was released: %q", state.ActiveEpochID)
	}
}

func TestCancelEpochRefusesInstallationBeforeFirstEntryCommit(t *testing.T) {
	ctx := context.Background()
	store := openServerDB(t)
	src := testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	epochID, batchID := mustID(t), mustID(t)
	if err := store.CreateEpoch(ctx, domain.Epoch{ID: epochID, SourceID: src.ID, Status: domain.EpochTransferring, TotalBatches: 1, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetState(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	state.ActiveEpochID = epochID
	if err = store.PutState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err = store.CreateBatch(ctx, domain.Batch{ID: batchID, EpochID: epochID, SourceID: src.ID, Sequence: 1, Status: domain.BatchTransferring, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = store.TransitionBatch(ctx, batchID, domain.BatchImporting, ""); err != nil {
		t.Fatal(err)
	}
	// Model a crash after the first repository write, but before its
	// imported_entries transaction. The durable batch status must suffice.
	if err = store.CancelEpoch(ctx, epochID, now, "cancel"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cancellation during installation must conflict: %v", err)
	}
	state, err = store.GetState(ctx, src.ID)
	if err != nil || state.ActiveEpochID != epochID {
		t.Fatalf("installation reservation was released: %+v, %v", state, err)
	}
	batch, err := store.GetBatch(ctx, batchID)
	if err != nil || batch.Status != domain.BatchImporting {
		t.Fatalf("batch was modified by rejected cancellation: %+v, %v", batch, err)
	}
}
