package apt

import (
	"context"
	"encoding/json"
	"path"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

func (a *Adapter) ManagedPaths(ctx context.Context, source domain.Source) (ports.ManagedPathMatcher, error) {
	_ = ctx
	cfg, err := parseConfig(source)
	if err != nil {
		return nil, err
	}
	var policy struct {
		GCOwnsPool bool `json:"gcOwnsPool"`
	}
	if len(source.ConfigJSON) != 0 {
		if err = json.Unmarshal(source.ConfigJSON, &policy); err != nil {
			return nil, err
		}
	}
	dist := path.Join("dists", cfg.Suite)
	return func(logical string) (bool, error) {
		if err := pack.ValidateLogicalPath(logical); err != nil {
			return false, err
		}
		if logical == dist || strings.HasPrefix(logical, dist+"/") {
			return true, nil
		}
		if policy.GCOwnsPool && (logical == "pool" || strings.HasPrefix(logical, "pool/")) {
			return true, nil
		}
		return false, nil
	}, nil
}

var _ ports.ManagedPathAdapter = (*Adapter)(nil)
