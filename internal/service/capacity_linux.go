//go:build linux

package service

import (
	"context"
	"syscall"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type FSCapacityInspector struct{}

func (FSCapacityInspector) Inspect(ctx context.Context, path string) (domain.Capacity, error) {
	_ = ctx
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil { return domain.Capacity{}, err }
	return domain.Capacity{Path:path, TotalBytes:st.Blocks*uint64(st.Bsize), FreeBytes:st.Bavail*uint64(st.Bsize)}, nil
}
