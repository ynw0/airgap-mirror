package common

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
)

type LocalRepository struct {
	Root       string
	parentReal map[string]string
}

func OpenLocalRepository(root string) (*LocalRepository, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("repository root is required: %w", domain.ErrInvalid)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repository root is not a directory: %w", domain.ErrInvalid)
	}
	return &LocalRepository{Root: real, parentReal: map[string]string{}}, nil
}

func withinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (r *LocalRepository) ResolveDirectory(logical string) (string, error) {
	if logical == "" {
		return r.Root, nil
	}
	if err := pack.ValidateLogicalPath(logical); err != nil {
		return "", err
	}
	target := filepath.Join(r.Root, filepath.FromSlash(logical))
	real, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	if !withinRoot(r.Root, real) {
		return "", fmt.Errorf("repository directory %s escapes root: %w", logical, domain.ErrInvalid)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository object %s is not a directory: %w", logical, domain.ErrInvalid)
	}
	return real, nil
}

func (r *LocalRepository) resolveParent(parent string) (string, error) {
	if real, ok := r.parentReal[parent]; ok {
		return real, nil
	}
	real, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	if !withinRoot(r.Root, real) {
		return "", fmt.Errorf("repository path escapes root: %w", domain.ErrInvalid)
	}
	r.parentReal[parent] = real
	return real, nil
}

func (r *LocalRepository) Resolve(logical string) (string, os.FileInfo, error) {
	if err := pack.ValidateLogicalPath(logical); err != nil {
		return "", nil, err
	}
	target := filepath.Join(r.Root, filepath.FromSlash(logical))
	parent, err := r.resolveParent(filepath.Dir(target))
	if err != nil {
		return "", nil, fmt.Errorf("resolve parent for %s: %w", logical, err)
	}
	candidate := filepath.Join(parent, filepath.Base(target))
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		candidate, err = filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", nil, err
		}
		if !withinRoot(r.Root, candidate) {
			return "", nil, fmt.Errorf("repository symlink %s escapes root: %w", logical, domain.ErrInvalid)
		}
		info, err = os.Stat(candidate)
		if err != nil {
			return "", nil, err
		}
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("repository object %s is not a regular file: %w", logical, domain.ErrInvalid)
	}
	return candidate, info, nil
}

func (r *LocalRepository) Open(logical string) (*os.File, os.FileInfo, error) {
	path, info, err := r.Resolve(logical)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return f, info, nil
}

func (r *LocalRepository) ReadFile(logical string, maxBytes int64) ([]byte, os.FileInfo, error) {
	f, info, err := r.Open(logical)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	if maxBytes > 0 && info.Size() > maxBytes {
		return nil, nil, fmt.Errorf("repository object %s exceeds %d bytes: %w", logical, maxBytes, domain.ErrInvalid)
	}
	body, err := io.ReadAll(f)
	return body, info, err
}

func (r *LocalRepository) SHA256(logical string) (string, int64, os.FileInfo, error) {
	f, info, err := r.Open(logical)
	if err != nil {
		return "", 0, nil, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, info, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, info, nil
}
