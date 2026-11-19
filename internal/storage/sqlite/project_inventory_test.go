package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ynw0/airgap-mirror/internal/adapters/pypi"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

func openResumableTestDB(t *testing.T) (*sql.DB, *ServerStore) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	if err := InitServer(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, NewServerStore(db)
}

func fixturePyPI(t *testing.T, store *ServerStore, count int) domain.Source {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("pkg-%04d", i)
		dir := filepath.Join(root, "simple", name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "packages"), 0755); err != nil {
			t.Fatal(err)
		}
		content := []byte(fmt.Sprintf("wheel #%d", i))
		file := name + ".whl"
		if err := os.WriteFile(filepath.Join(root, "packages", file), content, 0644); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		html := fmt.Sprintf("<html><a href=\"../../packages/%s#sha256=%s\">wheel</a></html>", file, hex.EncodeToString(digest[:]))
		if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(html), 0644); err != nil {
			t.Fatal(err)
		}
	}
	id, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	src := domain.Source{
		ID: id, Name: "large-pypi", Type: domain.SourcePyPI, Provider: pypi.Provider,
		RootPath: root, PublicURL: "https://repo.example/pypi",
		UpstreamURL: "https://pypi.org/simple", Enabled: true, ConfigJSON: json.RawMessage("{}"),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	return src
}

func TestPyPICheckpointResumesAndDoesNotReplaceLiveCatalogPrematurely(t *testing.T) {
	ctx := context.Background()
	_, store := openResumableTestDB(t)
	src := fixturePyPI(t, store, 102)
	oldHash := strings.Repeat("b", 64)
	old := domain.CatalogEntry{SourceID: src.ID, LogicalPath: "old/keep-until-complete", Size: 123, SHA256: oldHash, UpdatedAt: time.Now()}
	if err := store.Upsert(ctx, old); err != nil {
		t.Fatal(err)
	}
	factory := NewRebuildCatalogFactory(t.TempDir())
	sink, err := factory.OpenProjectInventory(ctx, "test-checkpoint", src.ID)
	if err != nil {
		t.Fatal(err)
	}
	adapter := pypi.New(nil)
	stop := errors.New("simulated Agent shutdown")
	_, err = adapter.Inventory(ctx, ports.InventoryRequest{
		Source: src, Current: store,
		Report: func(p ports.InventoryProgress) error {
			if p.CompletedProjects >= 100 {
				return stop
			}
			return nil
		},
	}, sink)
	if !errors.Is(err, stop) {
		t.Fatalf("expected interruption after 100 projects, got %v", err)
	}
	if err := sink.Abort(); err != nil {
		t.Fatal(err)
	}
	entry, err := store.Get(ctx, src.ID, old.LogicalPath)
	if err != nil || entry.SHA256 != oldHash {
		t.Fatalf("live catalog was changed on interrupted scan: %+v, %v", entry, err)
	}

	reopened, err := factory.OpenProjectInventory(ctx, "test-checkpoint", src.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := true
	var restored ports.InventoryProgress
	stats, err := adapter.Inventory(ctx, ports.InventoryRequest{
		Source: src, Current: store,
		Report: func(p ports.InventoryProgress) error {
			if first {
				restored = p
				first = false
			}
			return nil
		},
	}, reopened)
	if err != nil {
		t.Fatal(err)
	}
	if restored.CompletedProjects != 100 || restored.ScannedObjects != 200 || restored.LastProject != "pkg-0099" {
		t.Fatalf("did not restore saved checkpoint: %+v", restored)
	}
	if stats.Objects != 204 {
		t.Fatalf("expected 102 wheel files and 102 HTML pages, got %+v", stats)
	}
	if _, err := store.Get(ctx, src.ID, old.LogicalPath); err != nil {
		t.Fatalf("live catalog changed before final swap: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceCatalogFromFile(ctx, src.ID, reopened.Path(), stats, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, src.ID, old.LogicalPath); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stale live entry retained after replacement: %v", err)
	}
	if actual, err := store.Stats(ctx, src.ID); err != nil || actual.Objects != 204 {
		t.Fatalf("new catalog not installed: %+v %v", actual, err)
	}
	if err := factory.DiscardProjectInventory("test-checkpoint"); err != nil {
		t.Fatal(err)
	}
}

func TestPyPIRejectsChangedIndexBeforePublishingCheckpoint(t *testing.T) {
	ctx := context.Background()
	_, store := openResumableTestDB(t)
	src := fixturePyPI(t, store, 101)
	factory := NewRebuildCatalogFactory(t.TempDir())
	sink, err := factory.OpenProjectInventory(ctx, "changed-index", src.ID)
	if err != nil {
		t.Fatal(err)
	}
	adapter := pypi.New(nil)
	stop := errors.New("stop")
	_, err = adapter.Inventory(ctx, ports.InventoryRequest{Source: src, Report: func(p ports.InventoryProgress) error {
		if p.CompletedProjects >= 100 {
			return stop
		}
		return nil
	}}, sink)
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	_ = sink.Abort()
	page := filepath.Join(src.RootPath, "simple", "pkg-0000", "index.html")
	changed := []byte("<html>changed since previous checkpoint</html>")
	if err := os.WriteFile(page, changed, 0644); err != nil {
		t.Fatal(err)
	}
	// Also set a different size so file systems with coarse mtime precision detect it.
	reopened, err := factory.OpenProjectInventory(ctx, "changed-index", src.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Inventory(ctx, ports.InventoryRequest{Source: src}, reopened)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("index changes must prevent publishing stale content, got %v", err)
	}
	_ = reopened.Abort()
}

func TestMaintenanceSchemaMigratesOldStatusAndRows(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old-server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	if err = initSchema(ctx, db, serverSchema); err != nil {
		t.Fatal(err)
	}
	legacy := strings.ReplaceAll(maintenanceSchema, "'QUEUED','RUNNING','PAUSED','COMPLETED'", "'QUEUED','RUNNING','COMPLETED'")
	legacy = strings.ReplaceAll(legacy, "'QUEUED','RUNNING','PAUSED'", "'QUEUED','RUNNING'")
	legacy = strings.ReplaceAll(legacy, "  completed_projects INTEGER NOT NULL DEFAULT 0,\n  total_projects INTEGER NOT NULL DEFAULT 0,\n  last_project TEXT NOT NULL DEFAULT '',\n", "")
	if err = initSchema(ctx, db, legacy); err != nil {
		t.Fatal(err)
	}
	store := NewServerStore(db)
	src := fixturePyPI(t, store, 1)
	job := domain.MaintenanceJob{ID: "test-old-job", SourceID: src.ID, Kind: domain.MaintenanceInventory, Status: domain.MaintenanceQueued, CreatedAt: time.Now()}
	if err = store.CreateMaintenanceJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err = InitServer(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = store.TransitionMaintenanceJob(ctx, job.ID, domain.MaintenancePaused, "interrupted", time.Now()); err != nil {
		t.Fatal(err)
	}
	restored, err := store.GetMaintenanceJob(ctx, job.ID)
	if err != nil || restored.Status != domain.MaintenancePaused {
		t.Fatalf("migration damaged existing maintenance record: %+v %v", restored, err)
	}
	if err = InitServer(ctx, db); err != nil {
		t.Fatalf("upgrade not idempotent: %v", err)
	}
}

func TestStartupPausesDurablePyPIInventoryButNotLegacyJobs(t *testing.T) {
	ctx := context.Background()
	_, store := openResumableTestDB(t)
	src := fixturePyPI(t, store, 1)
	factory := NewRebuildCatalogFactory(t.TempDir())
	sink, err := factory.OpenProjectInventory(ctx, "crashed-inventory", src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sink.Restore(ctx, "test-source", "test-directory", 1); err != nil {
		t.Fatal(err)
	}
	if err = sink.Abort(); err != nil {
		t.Fatal(err)
	}
	other := fixturePyPI(t, store, 1)
	for _, test := range []struct {
		id       string
		sourceID string
	}{
		{id: "crashed-inventory", sourceID: src.ID},
		{id: "old-inventory", sourceID: other.ID},
	} {
		job := domain.MaintenanceJob{ID: test.id, SourceID: test.sourceID, Kind: domain.MaintenanceInventory, Status: domain.MaintenanceQueued, CreatedAt: time.Now()}
		if err := store.CreateMaintenanceJob(ctx, job); err != nil {
			t.Fatal(err)
		}
		if err := store.TransitionMaintenanceJob(ctx, test.id, domain.MaintenanceRunning, "", time.Now()); err != nil {
			t.Fatal(err)
		}
		if test.id == "crashed-inventory" {
			if err := store.SetMaintenanceCatalogPath(ctx, test.id, sink.Path()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.RecoverInterruptedMaintenanceJobs(ctx, time.Now(), "restart"); err != nil {
		t.Fatal(err)
	}
	paused, err := store.GetMaintenanceJob(ctx, "crashed-inventory")
	if err != nil || paused.Status != domain.MaintenancePaused {
		t.Fatalf("durable checkpoint not paused: %+v %v", paused, err)
	}
	failed, err := store.GetMaintenanceJob(ctx, "old-inventory")
	if err != nil || failed.Status != domain.MaintenanceFailed {
		t.Fatalf("old job should fail, got %+v %v", failed, err)
	}
}
