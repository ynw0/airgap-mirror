package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type GCCandidatesPage struct {
	Items []domain.GCCandidate `json:"items"`
	Stats domain.GCStats       `json:"stats"`
	Next  string               `json:"next"`
}

func (c *AgentClient) StartInventory(ctx context.Context, sourceID string) (domain.MaintenanceJob, error) {
	var out domain.MaintenanceJob
	err := c.jsonRequest(ctx, http.MethodPost, "/api/v1/sources/"+url.PathEscape(sourceID)+"/inventory", nil, &out)
	return out, err
}

func (c *AgentClient) StartGC(ctx context.Context, sourceID string, execute bool) (domain.MaintenanceJob, error) {
	var out domain.MaintenanceJob
	err := c.jsonRequest(ctx, http.MethodPost, "/api/v1/sources/"+url.PathEscape(sourceID)+"/gc", map[string]any{"execute": execute}, &out)
	return out, err
}

func (c *AgentClient) ListMaintenance(ctx context.Context, sourceID string, limit int) ([]domain.MaintenanceJob, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query := url.Values{"limit": []string{strconv.Itoa(limit)}}
	if sourceID != "" {
		query.Set("sourceId", sourceID)
	}
	var out []domain.MaintenanceJob
	err := c.jsonRequest(ctx, http.MethodGet, "/api/v1/maintenance?"+query.Encode(), nil, &out)
	return out, err
}

func (c *AgentClient) Maintenance(ctx context.Context, jobID string) (domain.MaintenanceJob, error) {
	var out domain.MaintenanceJob
	err := c.jsonRequest(ctx, http.MethodGet, "/api/v1/maintenance/"+url.PathEscape(jobID), nil, &out)
	return out, err
}

func (c *AgentClient) CancelMaintenance(ctx context.Context, jobID string) (domain.MaintenanceJob, error) {
	var out domain.MaintenanceJob
	err := c.jsonRequest(ctx, http.MethodPost, "/api/v1/maintenance/"+url.PathEscape(jobID)+"/cancel", nil, &out)
	return out, err
}

func (c *AgentClient) GCCandidates(ctx context.Context, jobID, after string, limit int) (GCCandidatesPage, error) {
	var out GCCandidatesPage
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query := url.Values{"limit": []string{strconv.Itoa(limit)}}
	if after != "" {
		query.Set("after", after)
	}
	err := c.jsonRequest(ctx, http.MethodGet, "/api/v1/maintenance/"+url.PathEscape(jobID)+"/candidates?"+query.Encode(), nil, &out)
	return out, err
}
