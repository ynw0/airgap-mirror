package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ynw0/airgap-mirror/internal/adapters/pypi"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

func TestIncrementalInventoryChecksCachedPackagesAndChangedPageDigest(t *testing.T) {
	ctx := context.Background()
	_, store := openResumableTestDB(t)
	src := fixturePyPI(t, store, 3)
	factory := NewRebuildCatalogFactory(t.TempDir())
	adapter := pypi.New(nil)
	first, err := factory.OpenProjectInventory(ctx, "initial", src.ID)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := adapter.Inventory(ctx, ports.InventoryRequest{Source: src, Current: store}, first)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if err = store.ReplaceCatalogFromFile(ctx, src.ID, first.Path(), stats, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = factory.PromoteProjectInventory("initial", src.ID); err != nil {
		t.Fatal(err)
	}

	// An unchanged page does not excuse a missing package or a size mismatch.
	pkg := filepath.Join(src.RootPath, "packages", "pkg-0000.whl")
	original, err := os.ReadFile(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(pkg); err != nil {
		t.Fatal(err)
	}
	missing, err := factory.OpenProjectInventory(ctx, "missing", src.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Inventory(ctx, ports.InventoryRequest{Source: src, Current: store, Incremental: true}, missing)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing cached package not detected: %v", err)
	}
	_ = missing.Discard()
	if err = os.WriteFile(pkg, append(append([]byte(nil), original...), 'x'), 0644); err != nil {
		t.Fatal(err)
	}
	wrong, err := factory.OpenProjectInventory(ctx, "wrong", src.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Inventory(ctx, ports.InventoryRequest{Source: src, Current: store, Incremental: true}, wrong)
	if !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "size changed") {
		t.Fatalf("size mismatch did not use the cached reference: %v", err)
	}
	_ = wrong.Discard()
	if err = os.WriteFile(pkg, original, 0644); err != nil {
		t.Fatal(err)
	}

	// A changed page with the same size and timestamp must be parsed again.
	page := filepath.Join(src.RootPath, "simple", "pkg-0001", "index.html")
	info, err := os.Stat(page)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	oldHash := shaOf("wheel #1")
	newHash := shaOf("new data")
	updated := strings.Replace(string(body), oldHash, newHash, 1)
	if updated == string(body) || len(updated) != len(body) {
		t.Fatal("invalid fixture")
	}
	if err = os.WriteFile(page, []byte(updated), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(page, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(src.RootPath, "packages", "pkg-0001.whl"), []byte("new data"), 0644); err != nil {
		t.Fatal(err)
	}
	// A removed project is not copied into the new catalog.
	if err = os.RemoveAll(filepath.Join(src.RootPath, "simple", "pkg-0002")); err != nil {
		t.Fatal(err)
	}
	next, err := factory.OpenProjectInventory(ctx, "incremental", src.ID)
	if err != nil {
		t.Fatal(err)
	}
	stats, err = adapter.Inventory(ctx, ports.InventoryRequest{Source: src, Current: store, Incremental: true}, next)
	if err != nil || stats.Objects != 4 {
		t.Fatalf("incremental result: %+v %v", stats, err)
	}
	if _, err = store.Get(ctx, src.ID, "packages/pkg-0002.whl"); err != nil {
		t.Fatal("live catalog changed before activation")
	}
	if err = next.Close(); err != nil {
		t.Fatal(err)
	}
	if err = store.ReplaceCatalogFromFile(ctx, src.ID, next.Path(), stats, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, src.ID, "packages/pkg-0001.whl")
	if err != nil || got.SHA256 != newHash {
		t.Fatalf("changed page was not parsed: %+v %v", got, err)
	}
	if _, err = store.Get(ctx, src.ID, "packages/pkg-0002.whl"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("removed project retained")
	}
	if err = factory.PromoteProjectInventory("incremental", src.ID); err != nil {
		t.Fatal(err)
	}
	cache, err := openInventoryCache(inventoryCachePath(factory.Root, src.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	var total int
	if err = cache.QueryRow("SELECT total_projects FROM inventory_checkpoint").Scan(&total); err != nil || total != 2 {
		t.Fatalf("new cache not promoted: %d %v", total, err)
	}
}

func TestIncrementalInventorySharedReferencesAreNotLost(t *testing.T) {
	ctx := context.Background()
	_, store := openResumableTestDB(t)
	src := fixturePyPI(t, store, 2)
	firstPage := filepath.Join(src.RootPath, "simple", "pkg-0000", "index.html")
	secondPage := filepath.Join(src.RootPath, "simple", "pkg-0001", "index.html")
	page, err := os.ReadFile(firstPage)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(secondPage, page, 0644); err != nil {
		t.Fatal(err)
	}
	factory := NewRebuildCatalogFactory(t.TempDir())
	adapter := pypi.New(nil)
	initial, err := factory.OpenProjectInventory(ctx, "shared", src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.Inventory(ctx, ports.InventoryRequest{Source: src}, initial); err != nil {
		t.Fatal(err)
	}
	if err = initial.Close(); err != nil {
		t.Fatal(err)
	}
	if err = factory.PromoteProjectInventory("shared", src.ID); err != nil {
		t.Fatal(err)
	}
	cache, err := openInventoryCache(inventoryCachePath(factory.Root, src.ID))
	if err != nil {
		t.Fatal(err)
	}
	var digest string
	err = cache.QueryRow("SELECT sha256 FROM inventory_reuse_pages WHERE project='pkg-0001'").Scan(&digest)
	_ = cache.Close()
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("incomplete shared reference set marked reusable: %v", err)
	}
	if err = os.RemoveAll(filepath.Dir(firstPage)); err != nil {
		t.Fatal(err)
	}
	next, err := factory.OpenProjectInventory(ctx, "shared-next", src.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Discard()
	stats, err := adapter.Inventory(ctx, ports.InventoryRequest{Source: src, Incremental: true}, next)
	if err != nil || stats.Objects != 2 {
		t.Fatalf("shared artifact lost when first project removed: %+v %v", stats, err)
	}
}

func TestResumedInventoryRejectsMissingPreviouslyScannedPackage(t *testing.T) {
	ctx := context.Background()
	_, store := openResumableTestDB(t)
	src := fixturePyPI(t, store, 101)
	factory := NewRebuildCatalogFactory(t.TempDir())
	adapter := pypi.New(nil)
	sink, err := factory.OpenProjectInventory(ctx, "paused", src.ID)
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop")
	_, err = adapter.Inventory(ctx, ports.InventoryRequest{Source: src, Incremental: true, Report: func(p ports.InventoryProgress) error {
		if p.CompletedProjects >= 100 {
			return stop
		}
		return nil
	}}, sink)
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if err = sink.Abort(); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(src.RootPath, "packages", "pkg-0000.whl")); err != nil {
		t.Fatal(err)
	}
	sink, err = factory.OpenProjectInventory(ctx, "paused", src.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Discard()
	_, err = adapter.Inventory(ctx, ports.InventoryRequest{Source: src, Incremental: true}, sink)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resumed scan did not detect missing file: %v", err)
	}
}
