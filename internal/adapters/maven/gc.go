package maven

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

func (a *GenericAdapter) ManagedPaths(ctx context.Context, source domain.Source) (ports.ManagedPathMatcher, error) {
	_ = ctx
	if _, err := parseGenericConfig(source); err != nil {
		return nil, err
	}
	var cfg genericInventoryConfig
	if err := json.Unmarshal(source.ConfigJSON, &cfg); err != nil {
		return nil, err
	}
	if cfg.InventoryMode != "filesystem" {
		return nil, nil
	}
	return func(logical string) (bool, error) {
		if err := pack.ValidateLogicalPath(logical); err != nil {
			return false, err
		}
		return true, nil
	}, nil
}

func (a *CentralAdapter) ManagedPaths(ctx context.Context, source domain.Source) (ports.ManagedPathMatcher, error) {
	if err := a.ValidateConfig(ctx, source); err != nil {
		return nil, err
	}
	return func(logical string) (bool, error) {
		if err := pack.ValidateLogicalPath(logical); err != nil {
			return false, err
		}
		parts := strings.Split(logical, "/")
		return len(parts) >= 4, nil
	}, nil
}

var _ ports.ManagedPathAdapter = (*GenericAdapter)(nil)
var _ ports.ManagedPathAdapter = (*CentralAdapter)(nil)
