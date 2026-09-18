package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func sourceFromInput(in SourceInput) (domain.Source, error) {
	cfg, err := json.Marshal(in.Config)
	if err != nil {
		return domain.Source{}, fmt.Errorf("encode source config: %w", err)
	}
	if in.Config == nil || string(cfg) == "null" {
		cfg = []byte("{}")
	}
	return domain.Source{
		ID:          strings.TrimSpace(in.ID),
		Name:        strings.TrimSpace(in.Name),
		Type:        domain.SourceType(strings.TrimSpace(in.Type)),
		Provider:    strings.TrimSpace(in.Provider),
		UpstreamURL: strings.TrimSpace(in.UpstreamURL),
		RootPath:    strings.TrimSpace(in.RootPath),
		PublicURL:   strings.TrimSpace(in.PublicURL),
		Enabled:     in.Enabled,
		ConfigJSON:  cfg,
	}, nil
}

func (a *App) CreateSource(in SourceInput) (AgentOverview, error) {
	agent, err := a.agentRef()
	if err != nil {
		return AgentOverview{}, err
	}
	src, err := sourceFromInput(in)
	if err != nil {
		return AgentOverview{}, err
	}
	if src.ID != "" {
		return AgentOverview{}, fmt.Errorf("create source must not include id: %w", domain.ErrInvalid)
	}
	if _, err = agent.CreateSource(a.ctx, src); err != nil {
		return AgentOverview{}, err
	}
	return loadAgentOverview(a.ctx, agent)
}

func (a *App) UpdateSource(in SourceInput) (AgentOverview, error) {
	agent, err := a.agentRef()
	if err != nil {
		return AgentOverview{}, err
	}
	src, err := sourceFromInput(in)
	if err != nil {
		return AgentOverview{}, err
	}
	if src.ID == "" {
		return AgentOverview{}, fmt.Errorf("source id is required: %w", domain.ErrInvalid)
	}
	if _, err = agent.UpdateSource(a.ctx, src); err != nil {
		return AgentOverview{}, err
	}
	return loadAgentOverview(a.ctx, agent)
}
