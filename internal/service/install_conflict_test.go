package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

func TestWriteVerifiedPublishedFilesAreGroupReadable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("published file mode is the Linux Apache data-plane contract")
	}
	root := t.TempDir()
	target := filepath.Join(root, "pkg", "a.jar")
	skipped, err := writeVerified(root, target, 4, shaHex("same"), bytes.NewReader([]byte("same")), false)
	if err != nil || skipped {
		t.Fatalf("write published file err=%v skipped=%v", err, skipped)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0664 {
		t.Fatalf("published mode=%o want=0664", info.Mode().Perm())
	}
	staging := filepath.Join(root, ".airgap-staging", "epoch", "a.jar")
	if _, err = writeVerified(root, staging, 4, shaHex("same"), bytes.NewReader([]byte("same")), false); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(staging)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("staging mode=%o want=0600", st.Mode().Perm())
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

func TestFileInstallerRejectsStagingPath(t *testing.T) {
	root := t.TempDir()
	_, err := (FileInstaller{}).Install(context.Background(), domain.Source{RootPath: root}, domain.PackRecord{
		LogicalPath: ".airgap-staging/epoch/a.jar", ContentLength: 1, SHA256: shaHex("x"),
	}, bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("staging artifact path should be rejected")
	}
}

func TestWriteVerifiedRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("same"), 0644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "artifact")
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	_, err := writeVerified(root, target, 4, shaHex("same"), bytes.NewReader([]byte("same")), false)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("symlink target should be rejected: %v", err)
	}
}
