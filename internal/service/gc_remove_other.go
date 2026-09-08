//go:build !linux

package service

import (
	"fmt"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func secureRemoveCandidate(root, logical string, candidate domain.GCCandidate) error {
	_ = root
	_ = logical
	_ = candidate
	return fmt.Errorf("physical GC deletion is supported only on Linux: %w", domain.ErrInvalid)
}
