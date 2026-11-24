package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ynw0/airgap-mirror/internal/adapters/common"
	"github.com/ynw0/airgap-mirror/internal/domain"
)

type repairStore interface {
	BeginRepairScan(context.Context, string, string) (domain.RepairCheckpoint, error)
	RepairCatalogPage(context.Context, string, string, int) ([]domain.CatalogEntry, error)
	CommitRepairPage(context.Context, string, string, string, int64, int64, []domain.RepairIssue) error
	FinishRepairScan(context.Context, string, time.Time) error
	ListRepairIssues(context.Context, string, string, int) ([]domain.RepairIssue, error)
}

func (r *MaintenanceRunner) StartRepair(ctx context.Context, sourceID string) (domain.MaintenanceJob, error) {
	var job domain.MaintenanceJob
	if _, ok := r.Jobs.(repairStore); !ok {
		return job, domain.ErrInvalid
	}
	source, err := r.Sources.GetSource(ctx, sourceID)
	if err != nil {
		return job, err
	}
	if source.Type != domain.SourcePyPI || source.Provider != "pypi" {
		return job, fmt.Errorf("缺失文件修复暂仅支持 PyPI: %w", domain.ErrInvalid)
	}
	state, err := r.requireIdleSource(ctx, sourceID)
	if err != nil {
		return job, err
	}
	if state.LiveObjects == 0 {
		return job, fmt.Errorf("正式文件账本为空，请先完成首次清点或导入；当前修复检查基于已有账本: %w", domain.ErrConflict)
	}
	id, err := domain.NewID()
	if err != nil {
		return job, err
	}
	job = domain.MaintenanceJob{ID: id, SourceID: sourceID, Kind: domain.MaintenanceRepair, Status: domain.MaintenanceQueued, CreatedAt: r.now()}
	if err = r.Jobs.CreateMaintenanceJob(ctx, job); err != nil {
		return job, err
	}
	return r.launchRepair(job, source)
}

func (r *MaintenanceRunner) resumeRepair(ctx context.Context, job domain.MaintenanceJob) (domain.MaintenanceJob, error) {
	if job.Status != domain.MaintenancePaused {
		return job, domain.ErrConflict
	}
	if _, ok := r.Jobs.(repairStore); !ok {
		return job, domain.ErrInvalid
	}
	source, err := r.Sources.GetSource(ctx, job.SourceID)
	if err != nil {
		return job, err
	}
	if source.Type != domain.SourcePyPI || source.Provider != "pypi" {
		return job, domain.ErrInvalid
	}
	if _, err = r.requireIdleSource(ctx, job.SourceID); err != nil {
		return job, err
	}
	return r.launchRepair(job, source)
}

func (r *MaintenanceRunner) launchRepair(job domain.MaintenanceJob, source domain.Source) (domain.MaintenanceJob, error) {
	r.mu.Lock()
	if r.cancels[job.ID] != nil {
		r.mu.Unlock()
		return job, fmt.Errorf("检查任务正在退出，请刷新后继续: %w", domain.ErrConflict)
	}
	ctx, cancel := context.WithCancel(r.root)
	r.cancels[job.ID] = cancel
	r.mu.Unlock()
	go r.runRepair(ctx, job, source)
	return job, nil
}

func (r *MaintenanceRunner) runRepair(ctx context.Context, job domain.MaintenanceJob, source domain.Source) {
	defer func() {
		r.mu.Lock()
		if cancel := r.cancels[job.ID]; cancel != nil {
			cancel()
		}
		delete(r.cancels, job.ID)
		delete(r.intents, job.ID)
		r.mu.Unlock()
	}()
	if err := r.transition(job.ID, domain.MaintenanceRunning, ""); err != nil {
		return
	}
	store := r.Jobs.(repairStore)
	if err := r.scanRepair(ctx, store, job, source); err != nil {
		r.finishInventoryError(job.ID, true, err)
	}
}

func (r *MaintenanceRunner) scanRepair(ctx context.Context, store repairStore, job domain.MaintenanceJob, source domain.Source) error {
	repo, err := common.OpenLocalRepository(source.RootPath)
	if err != nil {
		return err
	}
	cp, err := store.BeginRepairScan(ctx, job.ID, source.ID)
	if err != nil {
		return err
	}
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		entries, err := store.RepairCatalogPage(ctx, source.ID, cp.LastPath, 1000)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return store.FinishRepairScan(ctx, job.ID, r.now())
		}
		var bytes int64
		issues := make([]domain.RepairIssue, 0)
		for _, entry := range entries {
			if err = ctx.Err(); err != nil {
				return err
			}
			bytes += entry.Size
			_, info, statErr := repo.Resolve(entry.LogicalPath)
			issue := domain.RepairIssue{LogicalPath: entry.LogicalPath, PackageKey: entry.PackageKey, Size: entry.Size, SHA256: entry.SHA256}
			switch {
			case errors.Is(statErr, os.ErrNotExist):
				// Only explicit absence is repairable. Other errors must not
				// be mistaken for missing files (e.g. a permissions failure).
				if entry.PackageKey == "" || (!strings.HasPrefix(entry.LogicalPath, "packages/") && entry.LogicalPath != "simple/"+entry.PackageKey+"/index.html") {
					return fmt.Errorf("无法识别 PyPI 缺失文件所属项目 %s: %w", entry.LogicalPath, domain.ErrConflict)
				}
				issue.Kind = "MISSING"
				issue.ActualSize = -1
			case statErr != nil:
				return fmt.Errorf("检查 %s: %w", entry.LogicalPath, statErr)
			case info.Size() != entry.Size:
				issue.Kind = "SIZE_MISMATCH"
				issue.ActualSize = info.Size()
			default:
				continue
			}
			issues = append(issues, issue)
		}
		last := entries[len(entries)-1].LogicalPath
		if err = store.CommitRepairPage(ctx, job.ID, cp.LastPath, last, int64(len(entries)), bytes, issues); err != nil {
			return err
		}
		cp.LastPath = last
	}
}

func (r *MaintenanceRunner) RepairIssues(ctx context.Context, jobID, after string, limit int) ([]domain.RepairIssue, error) {
	job, err := r.Jobs.GetMaintenanceJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	store, ok := r.Jobs.(repairStore)
	if !ok || job.Kind != domain.MaintenanceRepair {
		return nil, domain.ErrInvalid
	}
	return store.ListRepairIssues(ctx, jobID, after, limit)
}
