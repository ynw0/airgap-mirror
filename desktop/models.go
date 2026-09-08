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
	Source   domain.Source      `json:"source"`
	State    domain.SourceState `json:"state"`
	Capacity domain.Capacity    `json:"capacity"`
}

type AgentOverview struct {
	Connected bool              `json:"connected"`
	BaseURL   string            `json:"baseUrl"`
	Sources   []AgentSourceView `json:"sources"`
}

type StateExportResult struct {
	Path    string              `json:"path"`
	Export  client.StateExport  `json:"export"`
}

type DesktopTransferResult struct {
	WorkspaceTracked bool                            `json:"workspaceTracked"`
	Local            *client.WorkspaceTransferResult `json:"local,omitempty"`
	Standalone       *client.BundleTransferResult    `json:"standalone,omitempty"`
}
