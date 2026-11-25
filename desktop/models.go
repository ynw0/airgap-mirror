package main

import (
	"github.com/ynw0/airgap-mirror/internal/client"
	"github.com/ynw0/airgap-mirror/internal/domain"
)

type Bootstrap struct {
	WorkspaceRoot string                `json:"workspaceRoot"`
	Capsules      []domain.StateCapsule `json:"capsules"`
}

type AgentSourceView struct {
	Source        domain.Source      `json:"source"`
	State         domain.SourceState `json:"state"`
	Capacity      domain.Capacity    `json:"capacity"`
	CapacityError string             `json:"capacityError,omitempty"`
}

type AgentOverview struct {
	Connected bool              `json:"connected"`
	BaseURL   string            `json:"baseUrl"`
	Sources   []AgentSourceView `json:"sources"`
}

// SourceInput 是桌面建源/改源的表单载荷；Config 由前端对象序列化。
type SourceInput struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Provider    string `json:"provider"`
	UpstreamURL string `json:"upstreamUrl"`
	RootPath    string `json:"rootPath"`
	PublicURL   string `json:"publicUrl"`
	Enabled     bool   `json:"enabled"`
	Config      any    `json:"config"`
}

type StateExportResult struct {
	Path   string             `json:"path"`
	Export client.StateExport `json:"export"`
}

type EpochView struct {
	Epoch   domain.Epoch   `json:"epoch"`
	Batches []domain.Batch `json:"batches"`
}

type DesktopTransferResult struct {
	WorkspaceTracked bool                            `json:"workspaceTracked"`
	Local            *client.WorkspaceTransferResult `json:"local,omitempty"`
	Standalone       *client.BundleTransferResult    `json:"standalone,omitempty"`
}
