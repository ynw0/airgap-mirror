package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestCatalogGenerationResumesCopyAndSwitchesAtomically(t *testing.T) {
	ctx := context.Background()
	store := openServerDB(t)
	src := testSource(t)
	other := testSource(t)
	now := time.Now()
	for _, s := range []domain.Source{src, other} {
		if err := store.CreateSource(ctx, s); err != nil {
			t.Fatal(err)
		}
		if err := store.Upsert(ctx, domain.CatalogEntry{SourceID: s.ID, LogicalPath: "old.bin", Size: 3, SHA256: shaOf("old"), UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "rebuild.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE catalog_entries(source_id TEXT NOT NULL,logical_path TEXT NOT NULL,size INTEGER NOT NULL,sha256 TEXT NOT NULL,package_key TEXT NOT NULL DEFAULT '',version TEXT NOT NULL DEFAULT '',attributes BLOB,updated_at TEXT NOT NULL,PRIMARY KEY(source_id,logical_path)) WITHOUT ROWID`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < catalogCopyBatch+1; i++ {
		if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_entries(source_id,logical_path,size,sha256,package_key,updated_at) VALUES(?,?,?,?,?,?)`, src.ID, fmt.Sprintf("new/%05d", i), 1, shaOf("x"), "project", timeString(now)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	expected := domain.CatalogStats{SourceID: src.ID, Objects: catalogCopyBatch + 1, Bytes: catalogCopyBatch + 1}
	job := domain.MaintenanceJob{ID: mustID(t), SourceID: src.ID, Kind: domain.MaintenanceInventory, Status: domain.MaintenanceQueued, CreatedAt: now}
	if err = store.CreateMaintenanceJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err = store.TransitionMaintenanceJob(ctx, job.ID, domain.MaintenanceRunning, "", now); err != nil {
		t.Fatal(err)
	}

	// Model an interruption after the first committed copy batch.
	conn, err := store.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, `ATTACH DATABASE ? AS rebuild`, path); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO catalog_generations(source_id,generation,expected_objects,expected_bytes,status) VALUES(?,?,?,?,'BUILDING')`, src.ID, job.ID, expected.Objects, expected.Bytes); err != nil {
		t.Fatal(err)
	}
	done, err := copyCatalogGenerationBatch(ctx, conn, src.ID, job.ID, expected)
	if err != nil || done {
		t.Fatalf("first batch: %v %v", done, err)
	}
	if _, err = conn.ExecContext(ctx, `DETACH DATABASE rebuild`); err != nil {
		t.Fatal(err)
	}
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Stats(ctx, src.ID)
	if err != nil || stats.Objects != 1 || stats.Bytes != 3 {
		t.Fatalf("unfinished generation visible: %+v %v", stats, err)
	}
	if _, err = store.Get(ctx, src.ID, "new/00000"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("staged rows visible")
	}
	if err = store.TransitionMaintenanceJob(ctx, job.ID, domain.MaintenancePaused, "interrupted", now); err != nil {
		t.Fatal(err)
	}
	if err = store.TransitionMaintenanceJob(ctx, job.ID, domain.MaintenanceRunning, "", now); err != nil {
		t.Fatal(err)
	}
	if err = store.PublishInventoryCatalog(ctx, job.ID, src.ID, path, expected, now); err != nil {
		t.Fatal(err)
	}
	completed, err := store.GetMaintenanceJob(ctx, job.ID)
	if err != nil || completed.Status != domain.MaintenanceCompleted {
		t.Fatalf("cutover did not complete job: %+v %v", completed, err)
	}
	stats, err = store.Stats(ctx, src.ID)
	if err != nil || stats != expected {
		t.Fatalf("wrong activated generation: %+v %v", stats, err)
	}
	if _, err = store.Get(ctx, src.ID, "old.bin"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("retired catalog visible")
	}
	if _, err = store.Get(ctx, other.ID, "old.bin"); err != nil {
		t.Fatal("other source changed")
	}
	var old int
	if err = store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM catalog_entries WHERE source_id=?", src.ID).Scan(&old); err != nil || old != 1 {
		t.Fatalf("legacy generation discarded: %d %v", old, err)
	}

	// All ordinary catalog paths and export must use the newly activated generation.
	entry := domain.CatalogEntry{SourceID: src.ID, LogicalPath: "post-cutover.bin", Size: 8, SHA256: shaOf("post"), PackageKey: "another", UpdatedAt: now}
	if err = store.Upsert(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if ok, e := store.Contains(ctx, src.ID, entry.LogicalPath); e != nil || !ok {
		t.Fatalf("membership: %v %v", ok, e)
	}
	if rows, e := store.ListByPackage(ctx, src.ID, "another"); e != nil || len(rows) != 1 {
		t.Fatalf("package lookup: %v %v", rows, e)
	}
	if err = store.Delete(ctx, src.ID, "new/00000"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "snapshot.sqlite")
	_, state, err := store.ExportSourceSnapshot(ctx, src.ID, dest)
	if err != nil || state.CatalogVersion != 1 {
		t.Fatalf("snapshot: %+v %v", state, err)
	}
	snapDB, snap, err := OpenCatalogSnapshot(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer snapDB.Close()
	if _, err = snap.Get(ctx, src.ID, entry.LogicalPath); err != nil {
		t.Fatal(err)
	}
	if _, err = snap.Get(ctx, src.ID, "new/00000"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("deleted record exported")
	}
}
