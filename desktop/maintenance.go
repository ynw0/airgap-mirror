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

func (a *App) PauseMaintenance(jobID string) (domain.MaintenanceJob, error) {
	agent, err := a.agentRef()
	if err != nil {
		return domain.MaintenanceJob{}, err
	}
	return agent.PauseMaintenance(a.ctx, jobID)
}

func (a *App) ResumeMaintenance(jobID string) (domain.MaintenanceJob, error) {
	agent, err := a.agentRef()
	if err != nil {
		return domain.MaintenanceJob{}, err
	}
	return agent.ResumeMaintenance(a.ctx, jobID)
}

func (a *App) StartRepair(sourceID string) (domain.MaintenanceJob, error) {
	agent, err := a.agentRef()
	if err != nil {
		return domain.MaintenanceJob{}, err
	}
	return agent.StartRepair(a.ctx, sourceID)
}

func (a *App) RepairIssues(jobID, after string, limit int) (mirrorclient.RepairIssuesPage, error) {
	agent, err := a.agentRef()
	if err != nil {
		return mirrorclient.RepairIssuesPage{}, err
	}
	return agent.RepairIssues(a.ctx, jobID, after, limit)
}

func (a *App) ExportRepairCapsule(sourceID, jobID, destination string) (StateExportResult, error) {
	agent, err := a.agentRef()
	if err != nil {
		return StateExportResult{}, err
	}
	exported, err := agent.ExportRepairState(a.ctx, sourceID, jobID)
	if err != nil {
		return StateExportResult{}, err
	}
	if err = agent.DownloadState(a.ctx, exported.Name, destination); err != nil {
		return StateExportResult{}, err
	}
	return StateExportResult{Path: destination, Export: exported}, nil
}
