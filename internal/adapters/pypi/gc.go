package pypi

import (
	"context"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

func (a *Adapter) ManagedPaths(ctx context.Context, source domain.Source) (ports.ManagedPathMatcher, error) {
	if err := a.ValidateConfig(ctx, source); err != nil {
		return nil, err
	}
	return func(logical string) (bool, error) {
		if err := pack.ValidateLogicalPath(logical); err != nil {
			return false, err
		}
		if strings.HasPrefix(logical, "packages/") {
			return true, nil
		}
		if !strings.HasPrefix(logical, "simple/") || !strings.HasSuffix(logical, "/index.html") {
			return false, nil
		}
		middle := strings.TrimSuffix(strings.TrimPrefix(logical, "simple/"), "/index.html")
		return middle != "" && !strings.Contains(middle, "/"), nil
	}, nil
}

var _ ports.ManagedPathAdapter = (*Adapter)(nil)
