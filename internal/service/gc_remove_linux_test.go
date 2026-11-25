//go:build linux

package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestSecureRemoveRejectsChangedFile(t *testing.T) {
	root := t.TempDir()
	logical := "pool/a.deb"
	path := filepath.Join(root, filepath.FromSlash(logical))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("abc"), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stale := domain.GCCandidate{LogicalPath: logical, Size: info.Size(), ModTimeUnixNano: info.ModTime().UnixNano()}
	time.Sleep(20 * time.Millisecond)
	if err = os.WriteFile(path, []byte("abcd"), 0644); err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	err = secureRemoveCandidate(root, logical, stale, rootInfo)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("TOCTOU change must abort delete, got %v", err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("file should still exist after TOCTOU abort")
	}
}

func TestSecureRemoveRejectsReplacedRepositoryRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(root, filepath.Join(parent, "old-repo")); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "package.whl")
	if err = os.WriteFile(target, []byte("replacement"), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	candidate := domain.GCCandidate{LogicalPath: "package.whl", Size: info.Size(), ModTimeUnixNano: info.ModTime().UnixNano()}
	if err = secureRemoveCandidate(root, candidate.LogicalPath, candidate, original); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("replaced root must conflict even if file matches: %v", err)
	}
	if _, err = os.Stat(target); err != nil {
		t.Fatalf("replacement repository file was removed: %v", err)
	}
}
