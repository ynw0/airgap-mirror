package main

import (
	mirrorclient "github.com/ynw0/airgap-mirror/internal/client"
	"github.com/ynw0/airgap-mirror/internal/domain"
)

func (a *App) StartInventory(sourceID string) (domain.MaintenanceJob, error) {
	agent, err := a.agentRef()
	if err != nil {
		return domain.MaintenanceJob{}, err
	}
	return agent.StartInventory(a.ctx, sourceID)
}

func (a *App) StartGC(sourceID string, execute bool) (domain.MaintenanceJob, error) {
	agent, err := a.agentRef()
	if err != nil {
		return domain.MaintenanceJob{}, err
	}
	return agent.StartGC(a.ctx, sourceID, execute)
}

func (a *App) ListMaintenance(sourceID string, limit int) ([]domain.MaintenanceJob, error) {
	agent, err := a.agentRef()
	if err != nil {
		return nil, err
	}
	return agent.ListMaintenance(a.ctx, sourceID, limit)
}

func (a *App) GetMaintenance(jobID string) (domain.MaintenanceJob, error) {
	agent, err := a.agentRef()
	if err != nil {
		return domain.MaintenanceJob{}, err
	}
	return agent.Maintenance(a.ctx, jobID)
}

func (a *App) CancelMaintenance(jobID string) (domain.MaintenanceJob, error) {
	agent, err := a.agentRef()
	if err != nil {
		return domain.MaintenanceJob{}, err
	}
	return agent.CancelMaintenance(a.ctx, jobID)
}

func (a *App) GCCandidates(jobID, after string, limit int) (mirrorclient.GCCandidatesPage, error) {
	agent, err := a.agentRef()
	if err != nil {
		return mirrorclient.GCCandidatesPage{}, err
	}
	return agent.GCCandidates(a.ctx, jobID, after, limit)
}
