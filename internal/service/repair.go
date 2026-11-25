package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	return r.startRepair(ctx, sourceID, 0)
}

func (r *MaintenanceRunner) StartIntegrity(ctx context.Context, sourceID string, mibPerSecond int) (domain.MaintenanceJob, error) {
	if mibPerSecond < 1 || mibPerSecond > 1024 {
		return domain.MaintenanceJob{}, fmt.Errorf("校验限速须在 1 至 1024 MiB/s 之间: %w", domain.ErrInvalid)
	}
	return r.startRepair(ctx, sourceID, mibPerSecond)
}

func (r *MaintenanceRunner) startRepair(ctx context.Context, sourceID string, mibPerSecond int) (domain.MaintenanceJob, error) {
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
	job = domain.MaintenanceJob{ID: id, SourceID: sourceID, Kind: domain.MaintenanceRepair, Status: domain.MaintenanceQueued, HashMiBPerSecond: mibPerSecond, CreatedAt: r.now()}
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
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
	limit := 1000
	if job.HashMiBPerSecond > 0 {
		limit = 32
	}
	// Reuse the read buffer for the entire scan.
	var hashBuffer []byte
	if job.HashMiBPerSecond > 0 {
		hashBuffer = make([]byte, 256*1024)
	}
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		entries, err := store.RepairCatalogPage(ctx, source.ID, cp.LastPath, limit)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return store.FinishRepairScan(ctx, job.ID, r.now())
		}
		var bytes int64
		issues := make([]domain.RepairIssue, 0)
		started := time.Now()
		last := ""
		count := int64(0)
		for _, entry := range entries {
			if err = ctx.Err(); err != nil {
				return err
			}
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
				if job.HashMiBPerSecond > 0 {
					sum, hashErr := hashRepositoryFile(ctx, repo, entry.LogicalPath, info, job.HashMiBPerSecond, hashBuffer)
					if hashErr != nil {
						return fmt.Errorf("校验 %s: %w", entry.LogicalPath, hashErr)
					}
					issue.ActualSHA256 = sum
				}
			default:
				if job.HashMiBPerSecond > 0 {
					sum, hashErr := hashRepositoryFile(ctx, repo, entry.LogicalPath, info, job.HashMiBPerSecond, hashBuffer)
					if hashErr != nil {
						return fmt.Errorf("校验 %s: %w", entry.LogicalPath, hashErr)
					}
					if !strings.EqualFold(sum, entry.SHA256) {
						issue.Kind = "HASH_MISMATCH"
						issue.ActualSize = info.Size()
						issue.ActualSHA256 = sum
					}
				}
			}
			if issue.Kind != "" {
				issues = append(issues, issue)
			}
			last = entry.LogicalPath
			count++
			bytes += entry.Size
			// Checkpoint only completed files. Revisit the current file after
			// interruption rather than persisting an unsafe partial digest.
			if job.HashMiBPerSecond > 0 && (bytes >= 256*1024*1024 || time.Since(started) >= 5*time.Second) {
				break
			}
		}
		if err = store.CommitRepairPage(ctx, job.ID, cp.LastPath, last, count, bytes, issues); err != nil {
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

// hashRepositoryFile limits read throughput and observes cancellation between
// chunks. Recheck inode, size and modification time before accepting the digest.
func hashRepositoryFile(ctx context.Context, repo *common.LocalRepository, logical string, expected os.FileInfo, mibPerSecond int, buffer []byte) (string, error) {
	if mibPerSecond < 1 || len(buffer) == 0 {
		return "", domain.ErrInvalid
	}
	f, _, err := repo.Open(logical)
	if err != nil {
		return "", err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(before, expected) || before.Size() != expected.Size() || !before.ModTime().Equal(expected.ModTime()) {
		return "", fmt.Errorf("文件在检查期间变化: %w", domain.ErrConflict)
	}
	h := sha256.New()
	var total int64
	rate := int64(mibPerSecond) * 1024 * 1024
	for {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		start := time.Now()
		n, readErr := f.Read(buffer)
		if n > 0 {
			total += int64(n)
			_, _ = h.Write(buffer[:n])
			delay := time.Duration(int64(n)*int64(time.Second)/rate) - time.Since(start)
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return "", ctx.Err()
				case <-timer.C:
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	after, err := f.Stat()
	if err != nil {
		return "", err
	}
	_, current, err := repo.Resolve(logical)
	if err != nil {
		return "", err
	}
	if total != before.Size() || !os.SameFile(before, after) || !os.SameFile(after, current) ||
		before.Size() != after.Size() || after.Size() != current.Size() ||
		!before.ModTime().Equal(after.ModTime()) || !after.ModTime().Equal(current.ModTime()) {
		return "", fmt.Errorf("文件在检查期间变化: %w", domain.ErrConflict)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
