package domain

import (
	"os"
	"path/filepath"
	"strings"
)

// RepositoryRootsOverlap compares configured paths and their resolved paths
// when available. An unmounted independent repository is not a conflict.
func RepositoryRootsOverlap(a, b string) (bool, error) {
	a, err := filepath.Abs(a)
	if err != nil {
		return false, err
	}
	b, err = filepath.Abs(b)
	if err != nil {
		return false, err
	}
	if pathsOverlap(a, b) {
		return true, nil
	}
	realA, errA := filepath.EvalSymlinks(a)
	realB, errB := filepath.EvalSymlinks(b)
	if errA == nil && errB == nil {
		if pathsOverlap(realA, realB) {
			return true, nil
		}
		infoA, statA := os.Stat(realA)
		infoB, statB := os.Stat(realB)
		return statA == nil && statB == nil && os.SameFile(infoA, infoB), nil
	}
	// A new or unmounted root may not exist yet. Resolve its existing
	// parent so that a path below a symlink is still compared correctly.
	if errA == nil {
		a = realA
	} else {
		a = resolveExistingParent(a)
	}
	if errB == nil {
		b = realB
	} else {
		b = resolveExistingParent(b)
	}
	return pathsOverlap(a, b), nil
}

func resolveExistingParent(path string) string {
	parent := filepath.Dir(path)
	if parent == path {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		return filepath.Join(resolved, filepath.Base(path))
	}
	return filepath.Join(resolveExistingParent(parent), filepath.Base(path))
}

func pathsOverlap(a, b string) bool {
	contains := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return contains(a, b) || contains(b, a)
}
