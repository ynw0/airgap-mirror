package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
)

// publishedFileMode 与 DEPLOYMENT 中仓库文件 0664 一致，供 Apache 同组读取。
const publishedFileMode os.FileMode = 0664

func underStaging(root, path string) bool {
	staging := filepath.Join(filepath.Clean(root), ".airgap-staging")
	clean := filepath.Clean(path)
	sep := string(filepath.Separator)
	return clean == staging || strings.HasPrefix(clean, staging+sep)
}

func chmodPublished(root, path string) error {
	if underStaging(root, path) {
		return nil
	}
	return os.Chmod(path, publishedFileMode)
}

func safeTarget(root, logical string) (string, error) {
	if err := pack.ValidateLogicalPath(logical); err != nil {
		return "", err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(absRoot, filepath.FromSlash(logical))
	rel, err := filepath.Rel(absRoot, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("logical path escapes source root: %w", domain.ErrInvalid)
	}
	return target, nil
}
func rejectSymlinkParents(root, target string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source root may not be symlink")
	}
	dir := filepath.Dir(target)
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err = os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink path component %s rejected", cur)
		}
	}
	return nil
}
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
func writeVerified(root, target string, size int64, want string, r io.Reader, replace bool) (bool, error) {
	if err := rejectSymlinkParents(root, target); err != nil {
		return false, err
	}
	if info, err := os.Stat(target); err == nil {
		h, n, e := hashFile(target)
		if e != nil {
			return false, e
		}
		if info.Size() == size && n == size && h == want {
			if err := chmodPublished(root, target); err != nil {
				return false, err
			}
			return true, nil
		}
		if !replace {
			return false, fmt.Errorf("target %s differs from incoming content: %w", target, domain.ErrConflict)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return false, err
	}
	if err := rejectSymlinkParents(root, target); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".airgap-write-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, size+1))
	if err != nil {
		return false, err
	}
	if n != size {
		return false, fmt.Errorf("content size %d != expected %d", n, size)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return false, fmt.Errorf("content sha256 %s != %s", got, want)
	}
	if err = tmp.Sync(); err != nil {
		return false, err
	}
	if err = tmp.Close(); err != nil {
		return false, err
	}
	if err = chmodPublished(root, tmpName); err != nil {
		return false, err
	}
	if replace {
		if err = os.Rename(tmpName, target); err != nil {
			return false, err
		}
		ok = true
	} else {
		// 临时文件与目标位于同一目录；hard-link 提供原子的 no-replace 语义。
		if err = os.Link(tmpName, target); err != nil {
			if errors.Is(err, os.ErrExist) {
				return false, fmt.Errorf("target appeared during install: %w", domain.ErrConflict)
			}
			return false, err
		}
		if err = os.Remove(tmpName); err != nil {
			return false, err
		}
		ok = true
	}
	if d, e := os.Open(filepath.Dir(target)); e == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return false, nil
}

// removeEpochStaging 只删除该 Epoch 的 metadata 暂存树；进行中的 Epoch 不受影响。
func removeEpochStaging(root, epochID string) error {
	logical := ".airgap-staging/" + epochID
	dir, err := safeTarget(root, logical)
	if err != nil {
		return err
	}
	if err = os.RemoveAll(dir); err != nil {
		return err
	}
	parent := filepath.Dir(dir)
	_ = os.Remove(parent)
	return nil
}

func removeStagingDir(path string) error {
	if path == "" {
		return nil
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return fmt.Errorf("staging path must be absolute: %w", domain.ErrInvalid)
	}
	if clean == filepath.Dir(clean) {
		return fmt.Errorf("refusing to remove filesystem root: %w", domain.ErrInvalid)
	}
	return os.RemoveAll(clean)
}

func refuseClosedEpoch(ep domain.Epoch) error {
	switch ep.Status {
	case domain.EpochCancelled, domain.EpochFailed, domain.EpochComplete:
		return fmt.Errorf("epoch %s is %s: %w", ep.ID, ep.Status, domain.ErrConflict)
	default:
		return nil
	}
}
