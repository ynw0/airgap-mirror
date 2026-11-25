package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2/pkg/runtime"
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

func (a *App) ListExportJobs() ([]domain.ExportJob, error) {
	agent, err := a.agentRef()
	if err != nil {
		return nil, err
	}
	return agent.ListExportJobs(a.ctx)
}

func (a *App) DownloadExportJob(id, destination string) error {
	agent, err := a.agentRef()
	if err != nil {
		return err
	}
	return agent.DownloadExportJob(a.ctx, id, destination)
}

func (a *App) ReclaimCatalogHistory(sourceID string) (domain.CatalogReclaimResult, error) {
	agent, err := a.agentRef()
	if err != nil {
		return domain.CatalogReclaimResult{}, err
	}
	return agent.ReclaimCatalogHistory(a.ctx, sourceID)
}

func (a *App) StartIntegrity(sourceID string, mibPerSecond int) (domain.MaintenanceJob, error) {
	agent, err := a.agentRef()
	if err != nil {
		return domain.MaintenanceJob{}, err
	}
	return agent.StartIntegrity(a.ctx, sourceID, mibPerSecond)
}

func (a *App) ApproveRepairReplacement(jobID string, approve bool) error {
	agent, err := a.agentRef()
	if err != nil {
		return err
	}
	return agent.ApproveRepairReplacement(a.ctx, jobID, approve)
}

func (a *App) StartIncrementalInventory(sourceID string) (domain.MaintenanceJob, error) {
	agent, err := a.agentRef()
	if err != nil {
		return domain.MaintenanceJob{}, err
	}
	return agent.StartIncrementalInventory(a.ctx, sourceID)
}

type RepairReportResult struct {
	Path  string `json:"path"`
	Count int64  `json:"count"`
}

func (a *App) ExportCompleteRepairReport(jobID string) (RepairReportResult, error) {
	var result RepairReportResult
	agent, err := a.agentRef()
	if err != nil {
		return result, err
	}
	destination, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "保存完整异常报告",
		DefaultFilename: "airgap-repair-full.json",
		Filters:         []runtime.FileFilter{{DisplayName: "JSON (*.json)", Pattern: "*.json"}},
	})
	if err != nil || destination == "" {
		return result, err
	}
	if filepath.Ext(destination) == "" {
		destination += ".json"
	}
	a.opsMu.Lock()
	if _, exists := a.reportCancels[jobID]; exists {
		a.opsMu.Unlock()
		return result, fmt.Errorf("report export is already running: %w", domain.ErrConflict)
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.reportCancels[jobID] = cancel
	a.opsMu.Unlock()
	defer func() {
		cancel()
		a.opsMu.Lock()
		delete(a.reportCancels, jobID)
		a.opsMu.Unlock()
	}()
	// Write beside the destination so a crash cannot leave a partial report
	// with the final name, and publication does not cross filesystems.
	if _, err = os.Lstat(destination); err == nil {
		return result, fmt.Errorf("report destination already exists: %w", os.ErrExist)
	} else if !os.IsNotExist(err) {
		return result, err
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".airgap-repair-*.partial")
	if err != nil {
		return result, err
	}
	staged := file.Name()
	defer os.Remove(staged)
	count, writeErr := agent.WriteRepairReport(ctx, jobID, file, func(done, total int64) {
		runtime.EventsEmit(a.ctx, "repair-report-progress", map[string]any{"jobId": jobID, "done": done, "total": total})
	})
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return result, writeErr
	}
	if closeErr != nil {
		return result, closeErr
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = publishReportFile(staged, destination); err != nil {
		return result, fmt.Errorf("publish completed report: %w", err)
	}
	return RepairReportResult{Path: destination, Count: count}, nil
}

func (a *App) CancelRepairReport(jobID string) error {
	a.opsMu.Lock()
	cancel, ok := a.reportCancels[jobID]
	a.opsMu.Unlock()
	if !ok {
		return fmt.Errorf("report export is not running: %w", domain.ErrNotFound)
	}
	cancel()
	return nil
}
