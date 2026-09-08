//go:build !linux

package service

import (
	"context"
	"fmt"
	"github.com/ynw0/airgap-mirror/internal/domain"
)

type FSCapacityInspector struct{}

func (FSCapacityInspector) Inspect(context.Context, string) (domain.Capacity, error) {
	return domain.Capacity{}, fmt.Errorf("filesystem capacity inspection is only supported on linux server")
}
