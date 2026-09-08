//go:build linux

package service

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
)

func secureRemoveCandidate(root, logical string, candidate domain.GCCandidate) error {
	if err := pack.ValidateLogicalPath(logical); err != nil {
		return err
	}
	parts := strings.Split(logical, "/")
	if len(parts) == 0 {
		return fmt.Errorf("empty GC path: %w", domain.ErrInvalid)
	}
	fd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	parent := fd
	ownedParent := false
	defer func() {
		if ownedParent {
			_ = unix.Close(parent)
		}
	}()
	for _, segment := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(parent, segment, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return fmt.Errorf("open GC path component %s: %w", segment, openErr)
		}
		if ownedParent {
			_ = unix.Close(parent)
		}
		parent = next
		ownedParent = true
	}
	name := parts[len(parts)-1]
	var st unix.Stat_t
	if err = unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("GC target %s is no longer a regular file: %w", logical, domain.ErrConflict)
	}
	mtime := st.Mtim.Sec*1_000_000_000 + st.Mtim.Nsec
	if st.Size != candidate.Size || mtime != candidate.ModTimeUnixNano {
		return fmt.Errorf("GC target %s changed after planning: %w", logical, domain.ErrConflict)
	}
	if err = unix.Unlinkat(parent, name, 0); err != nil {
		return err
	}
	return nil
}
