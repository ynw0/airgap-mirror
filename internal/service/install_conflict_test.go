package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func shaHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestWriteVerifiedRejectsDifferentContent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "pool", "a.deb")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := writeVerified(root, target, 3, shaHex("new"), bytes.NewReader([]byte("new")), false)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected hard conflict, got %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("target overwritten: %q", got)
	}
}

func TestWriteVerifiedSkipsIdenticalContent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "pool", "a.deb")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("same"), 0644); err != nil {
		t.Fatal(err)
	}
	skipped, err := writeVerified(root, target, 4, shaHex("same"), bytes.NewReader([]byte("same")), false)
	if err != nil || !skipped {
		t.Fatalf("identical content should skip err=%v skipped=%v", err, skipped)
	}
}

func TestFileInstallerRejectsUnsafePath(t *testing.T) {
	root := t.TempDir()
	_, err := (FileInstaller{}).Install(context.Background(), domain.Source{RootPath: root}, domain.PackRecord{
		LogicalPath: "../escape", ContentLength: 1, SHA256: shaHex("x"),
	}, bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("path traversal should be rejected")
	}
}
