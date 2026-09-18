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
	err = secureRemoveCandidate(root, logical, stale)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("TOCTOU change must abort delete, got %v", err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("file should still exist after TOCTOU abort")
	}
}
