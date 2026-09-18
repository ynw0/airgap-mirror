package sqlite

import (
	"context"
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
