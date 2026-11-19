package sqlite

import (
	"context"
	"errors"
	"testing"

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
