package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type ExportJobStore interface {
	CreateExportJob(context.Context, domain.ExportJob) error
	GetExportJob(context.Context, string) (domain.ExportJob, error)
	ListExportJobs(context.Context) ([]domain.ExportJob, error)
	RecoverExportJobs(context.Context) error
	ClaimExportJob(context.Context) (domain.ExportJob, error)
	UpdateExportJob(context.Context, string, string, int) error
	FinishExportJob(context.Context, string, *domain.StateCapsule, string) error
}

type ExportJobs struct {
	Store    ExportJobStore
	Capsules CapsuleService
}

func (e *ExportJobs) Enqueue(ctx context.Context, ids []string, repair string) (domain.ExportJob, error) {
	var j domain.ExportJob
	if repair != "" && len(ids) != 1 {
		return j, domain.ErrInvalid
	}
	if len(ids) == 0 {
		sources, err := e.Capsules.Store.ListSources(ctx)
		if err != nil {
			return j, err
		}
		for _, s := range sources {
			ids = append(ids, s.ID)
		}
	}
	if len(ids) == 0 || len(ids) > 1000 {
		return j, domain.ErrInvalid
	}
	seen := make(map[string]bool)
	for _, id := range ids {
		if id == "" || seen[id] {
			return j, domain.ErrInvalid
		}
		seen[id] = true
		if _, err := e.Capsules.Store.GetSource(ctx, id); err != nil {
			return j, err
		}
	}
	id, err := domain.NewID()
	if err != nil {
		return j, err
	}
	now := time.Now()
	j = domain.ExportJob{ID: id, SourceIDs: ids, RepairJobID: repair, Status: "QUEUED", Stage: "等待开始", TotalSources: len(ids), CreatedAt: now, UpdatedAt: now}
	return j, e.Store.CreateExportJob(ctx, j)
}

// Run is a single worker. Disconnecting the initiating browser does not cancel
// the export. On a process restart unfinished exports are rebuilt from scratch
// with the same job ID; finished exports are never rebuilt.
func (e *ExportJobs) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		j, err := e.Store.ClaimExportJob(ctx)
		if err == nil {
			cap, _, runErr := e.Capsules.exportJob(ctx, j.SourceIDs, j.RepairJobID, j.ID, func(stage string, done int) error {
				return e.Store.UpdateExportJob(ctx, j.ID, stage, done)
			})
			if ctx.Err() != nil {
				return
			} // startup recovery requeues the unfinished job
			var result *domain.StateCapsule
			message := ""
			if runErr == nil {
				result = &cap
			} else {
				message = runErr.Error()
			}
			// A transient database lock must not silently strand a finished job.
			for {
				err = e.Store.FinishExportJob(ctx, j.ID, result, message)
				if err == nil {
					break
				}
				log.Printf("finish export job %s: %v", j.ID, err)
				if errors.Is(err, domain.ErrConflict) {
					break
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
			continue
		}
		if !errors.Is(err, domain.ErrNotFound) {
			log.Printf("claim state export: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (e *ExportJobs) Get(ctx context.Context, id string) (domain.ExportJob, error) {
	if id == "" {
		return domain.ExportJob{}, fmt.Errorf("job id required: %w", domain.ErrInvalid)
	}
	return e.Store.GetExportJob(ctx, id)
}
