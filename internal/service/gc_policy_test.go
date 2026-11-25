package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ynw0/airgap-mirror/internal/domain"
	storesqlite "github.com/ynw0/airgap-mirror/internal/storage/sqlite"
)

func TestRootsOverlapNestedAndDistinct(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "nested")
	other := t.TempDir()
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	if !rootsOverlap(parent, child) || !rootsOverlap(child, parent) {
		t.Fatal("nested roots must overlap")
	}
	if rootsOverlap(parent, other) {
		t.Fatal("sibling temp dirs must not overlap")
	}
}

func TestExclusiveRootRejectsNestedSources(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "nested")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if err = storesqlite.InitServer(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := storesqlite.NewServerStore(db)
	now := time.Now().UTC()
	srcA := domain.Source{ID: "a", Name: "a", Type: domain.SourceAPT, Provider: "apt-generic", UpstreamURL: "https://example.com/debian", RootPath: parent, PublicURL: "https://repo.internal/a", Enabled: true, ConfigJSON: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now}
	srcB := srcA
	srcB.ID, srcB.Name, srcB.RootPath, srcB.PublicURL = "b", "b", child, "https://repo.internal/b"
	if err = store.CreateSource(ctx, srcA); err != nil {
		t.Fatal(err)
	}
	if err = store.CreateSource(ctx, srcB); err != nil {
		t.Fatal(err)
	}
	runner := NewMaintenanceRunner(ctx, store, store, store, nil, nil)
	if _, err = runner.exclusiveRoot(ctx, srcA); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("nested roots should be refused, got %v", err)
	}
	// Disabled sources still own repository data.
	srcB.Enabled = false
	if err = store.UpdateSource(ctx, srcB); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.exclusiveRoot(ctx, srcA); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("disabled overlapping source must still be protected: %v", err)
	}
	distinct := t.TempDir()
	srcB.RootPath = distinct
	if err = store.UpdateSource(ctx, srcB); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.exclusiveRoot(ctx, srcA); err != nil {
		t.Fatalf("distinct roots should be allowed: %v", err)
	}
	srcB.RootPath = filepath.Join(distinct, "not-mounted")
	if err = store.UpdateSource(ctx, srcB); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.exclusiveRoot(ctx, srcA); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("unresolved repository root must not be assumed independent: %v", err)
	}
	alias := filepath.Join(distinct, "alias")
	if err = os.Symlink(parent, alias); err != nil {
		t.Skipf("symlink fixture not supported: %v", err)
	}
	srcB.RootPath = alias
	if err = store.UpdateSource(ctx, srcB); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.exclusiveRoot(ctx, srcA); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("symlink alias must be refused: %v", err)
	}
}

func TestPhysicalGCExecuteRejectedOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux 物理删除由 gc_remove_linux_test.go 覆盖")
	}
	err := secureRemoveCandidate(t.TempDir(), "pool/a.deb", domain.GCCandidate{LogicalPath: "pool/a.deb", Size: 1}, nil)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("non-Linux physical GC must be refused, got %v", err)
	}
}

func TestAPTPublishPriorityInReleaseLast(t *testing.T) {
	inrelease := publishPriority(domain.SourceAPT, "dists/bookworm/InRelease")
	release := publishPriority(domain.SourceAPT, "dists/bookworm/Release")
	deb := publishPriority(domain.SourceAPT, "pool/main/a.deb")
	if !(deb < release && release < inrelease) {
		t.Fatalf("APT publish order deb=%d release=%d inrelease=%d", deb, release, inrelease)
	}
}
