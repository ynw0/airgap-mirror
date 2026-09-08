package service

import (
	"fmt"
	"os"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

// OpenPublishedFile opens a repository file only after applying the same root
// containment and symlink-parent checks used by the installer/publisher.
func OpenPublishedFile(root, logical string) (*os.File, os.FileInfo, error) {
	target, err := safeTarget(root, logical)
	if err != nil {
		return nil, nil, err
	}
	if err = rejectSymlinkParents(root, target); err != nil {
		return nil, nil, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("published path is not a regular file: %w", domain.ErrConflict)
	}
	f, err := os.Open(target)
	if err != nil {
		return nil, nil, err
	}
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, nil, fmt.Errorf("published file changed while opening: %w", domain.ErrConflict)
	}
	return f, opened, nil
}
