package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func writeRebuildCatalog(t *testing.T, sourceID string, path string, size int64) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "catalog-rebuild.sqlite")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = db.ExecContext(ctx, `CREATE TABLE catalog_entries(source_id TEXT NOT NULL,logical_path TEXT NOT NULL,size INTEGER NOT NULL,sha256 TEXT NOT NULL,package_key TEXT NOT NULL DEFAULT '',version TEXT NOT NULL DEFAULT '',attributes BLOB,updated_at TEXT NOT NULL,PRIMARY KEY(source_id,logical_path))`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO catalog_entries(source_id,logical_path,size,sha256,updated_at) VALUES(?,?,?,?,?)`, sourceID, path, size, shaOf("x"), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestReplaceCatalogAtomicAndBumpsVersion(t *testing.T) {
	store := openServerDB(t)
	ctx := context.Background()
	src := testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(ctx, domain.CatalogEntry{SourceID: src.ID, LogicalPath: "old.bin", Size: 3, SHA256: shaOf("old"), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	rebuild := writeRebuildCatalog(t, src.ID, "new.bin", 7)
	if err := store.ReplaceCatalogFromFile(ctx, src.ID, rebuild, domain.CatalogStats{SourceID: src.ID, Bytes: 7, Objects: 1}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, src.ID, "old.bin"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old catalog row should be gone, got %v", err)
	}
	got, err := store.Get(ctx, src.ID, "new.bin")
	if err != nil || got.Size != 7 {
		t.Fatalf("new catalog missing: %+v %v", got, err)
	}
	state, err := store.GetState(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.CatalogVersion != 1 || state.LiveObjects != 1 || state.LiveBytes != 7 {
		t.Fatalf("state after replace: %+v", state)
	}
}

func TestReplaceCatalogRejectsStatsMismatch(t *testing.T) {
	store := openServerDB(t)
	ctx := context.Background()
	src := testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	rebuild := writeRebuildCatalog(t, src.ID, "new.bin", 7)
	err := store.ReplaceCatalogFromFile(ctx, src.ID, rebuild, domain.CatalogStats{SourceID: src.ID, Bytes: 99, Objects: 1}, time.Now().UTC())
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected stats conflict, got %v", err)
	}
	stats, err := store.Stats(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Objects != 0 {
		t.Fatalf("live catalog mutated on failed replace: %+v", stats)
	}
}

func TestReplaceCatalogBlockedByActiveEpoch(t *testing.T) {
	store := openServerDB(t)
	ctx := context.Background()
	src := testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	if err := store.PutState(ctx, domain.SourceState{SourceID: src.ID, ActiveEpochID: mustID(t), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	rebuild := writeRebuildCatalog(t, src.ID, "new.bin", 7)
	err := store.ReplaceCatalogFromFile(ctx, src.ID, rebuild, domain.CatalogStats{SourceID: src.ID, Bytes: 7, Objects: 1}, time.Now().UTC())
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected active-epoch conflict, got %v", err)
	}
}

func TestRebuildCatalogAbortLeavesNoFile(t *testing.T) {
	ctx := context.Background()
	factory := NewRebuildCatalogFactory(t.TempDir())
	sink, err := factory.Create(ctx, mustID(t), "src-1")
	if err != nil {
		t.Fatal(err)
	}
	path := sink.Path()
	if err = sink.Put(ctx, domain.CatalogEntry{SourceID: "src-1", LogicalPath: "a.bin", Size: 1, SHA256: shaOf("a"), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err = sink.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err == nil {
		t.Fatal("aborted rebuild catalog file still exists")
	}
}
