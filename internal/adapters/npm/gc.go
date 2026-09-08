package npm

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
		if logical == "packuments" || strings.HasPrefix(logical, "packuments/") {
			return strings.HasSuffix(strings.ToLower(logical), ".json") || logical == "packuments", nil
		}
		return strings.HasSuffix(strings.ToLower(logical), ".tgz"), nil
	}, nil
}

var _ ports.ManagedPathAdapter = (*Adapter)(nil)
