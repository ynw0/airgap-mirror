package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func openServerDB(t *testing.T) *ServerStore {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if err = InitServer(ctx, db); err != nil {
		t.Fatal(err)
	}
	return NewServerStore(db)
}

func testSource(t *testing.T) domain.Source {
	t.Helper()
	id, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return domain.Source{
		ID:          id,
		Name:        "apt-test",
		Type:        domain.SourceAPT,
		Provider:    "apt-generic",
		UpstreamURL: "https://deb.debian.org/debian",
		RootPath:    t.TempDir(),
		PublicURL:   "https://repo.internal/apt",
		Enabled:     true,
		ConfigJSON:  json.RawMessage(`{"suite":"bookworm","releaseMode":"inrelease"}`),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func TestCatalogSnapshotRejectsWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = db.ExecContext(ctx, `CREATE TABLE catalog_entries(source_id TEXT NOT NULL,logical_path TEXT NOT NULL,size INTEGER NOT NULL,sha256 TEXT NOT NULL,package_key TEXT NOT NULL DEFAULT '',version TEXT NOT NULL DEFAULT '',attributes BLOB,updated_at TEXT NOT NULL,PRIMARY KEY(source_id,logical_path))`)
	if err != nil {
		t.Fatal(err)
	}
	sourceID := "src-1"
	_, err = db.ExecContext(ctx, `INSERT INTO catalog_entries(source_id,logical_path,size,sha256,updated_at) VALUES(?,?,?,?,?)`, sourceID, "pool/a.deb", 4, strings.Repeat("a", 64), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	snapDB, snap, err := OpenCatalogSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapDB.Close() })
	entry, err := snap.Get(ctx, sourceID, "pool/a.deb")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Size != 4 {
		t.Fatalf("size=%d", entry.Size)
	}
	if err = snap.Upsert(ctx, entry); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("upsert should be read-only, got %v", err)
	}
	if err = snap.Delete(ctx, sourceID, "pool/a.deb"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("delete should be read-only, got %v", err)
	}
}

func TestCatalogSnapshotValidateSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = db.ExecContext(ctx, `CREATE TABLE catalog_entries(source_id TEXT NOT NULL,logical_path TEXT NOT NULL,size INTEGER NOT NULL,sha256 TEXT NOT NULL,package_key TEXT NOT NULL DEFAULT '',version TEXT NOT NULL DEFAULT '',attributes BLOB,updated_at TEXT NOT NULL,PRIMARY KEY(source_id,logical_path))`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, `INSERT INTO catalog_entries(source_id,logical_path,size,sha256,updated_at) VALUES(?,?,?,?,?)`, "src-a", "a.bin", 1, strings.Repeat("b", 64), now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO catalog_entries(source_id,logical_path,size,sha256,updated_at) VALUES(?,?,?,?,?)`, "src-b", "b.bin", 1, strings.Repeat("c", 64), now)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	snapDB, snap, err := OpenCatalogSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapDB.Close() })
	if err = snap.ValidateSource(ctx, "src-a"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("mixed catalog should conflict, got %v", err)
	}
}

func TestMaintenanceBlockedByActiveEpoch(t *testing.T) {
	store := openServerDB(t)
	ctx := context.Background()
	src := testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	epochID, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PutState(ctx, domain.SourceState{SourceID: src.ID, ActiveEpochID: epochID, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	jobID, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	err = store.CreateMaintenanceJob(ctx, domain.MaintenanceJob{
		ID:        jobID,
		SourceID:  src.ID,
		Kind:      domain.MaintenanceInventory,
		Status:    domain.MaintenanceQueued,
		CreatedAt: time.Now().UTC(),
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestEpochBlockedByActiveMaintenance(t *testing.T) {
	store := openServerDB(t)
	ctx := context.Background()
	src := testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	jobID, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateMaintenanceJob(ctx, domain.MaintenanceJob{
		ID:        jobID,
		SourceID:  src.ID,
		Kind:      domain.MaintenanceGC,
		Status:    domain.MaintenanceQueued,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	epochID, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	err = store.PutState(ctx, domain.SourceState{SourceID: src.ID, ActiveEpochID: epochID, UpdatedAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("active maintenance should block epoch activation")
	}
	state, err := store.GetState(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveEpochID != "" {
		t.Fatalf("active epoch leaked: %s", state.ActiveEpochID)
	}
}

func TestDuplicateActiveMaintenanceRejected(t *testing.T) {
	store := openServerDB(t)
	ctx := context.Background()
	src := testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	id1, _ := domain.NewID()
	id2, _ := domain.NewID()
	job := domain.MaintenanceJob{ID: id1, SourceID: src.ID, Kind: domain.MaintenanceInventory, Status: domain.MaintenanceQueued, CreatedAt: now}
	if err := store.CreateMaintenanceJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	job.ID = id2
	err := store.CreateMaintenanceJob(ctx, job)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second active job should conflict, got %v", err)
	}
}

func TestCatalogSnapshotFileIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE catalog_entries(source_id TEXT NOT NULL,logical_path TEXT NOT NULL,size INTEGER NOT NULL,sha256 TEXT NOT NULL,package_key TEXT NOT NULL DEFAULT '',version TEXT NOT NULL DEFAULT '',attributes BLOB,updated_at TEXT NOT NULL,PRIMARY KEY(source_id,logical_path))`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	snapDB, _, err := OpenCatalogSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapDB.Close() })
	_, err = snapDB.Exec(`INSERT INTO catalog_entries(source_id,logical_path,size,sha256,updated_at) VALUES('x','y',1,'z','t')`)
	if err == nil {
		t.Fatal("readonly snapshot connection should reject SQL writes")
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
