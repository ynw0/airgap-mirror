package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func (c *AgentClient) Epoch(ctx context.Context, epochID string) (domain.Epoch, error) {
	var out domain.Epoch
	err := c.jsonRequest(ctx, http.MethodGet, "/api/v1/epochs/"+url.PathEscape(epochID), nil, &out)
	return out, err
}
