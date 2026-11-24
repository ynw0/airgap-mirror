package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

func scanMaintenanceJob(row interface{ Scan(...any) error }) (domain.MaintenanceJob, error) {
	var job domain.MaintenanceJob
	var execute int
	var created string
	var started, completed sql.NullString
	if err := row.Scan(
		&job.ID,
		&job.SourceID,
		&job.Kind,
		&job.Status,
		&execute,
		&job.HashMiBPerSecond,
		&job.ReplacementApproved,
		&job.ScannedObjects,
		&job.ScannedBytes,
		&job.CompletedProjects,
		&job.TotalProjects,
		&job.LastProject,
		&job.CandidateObjects,
		&job.CandidateBytes,
		&job.AffectedObjects,
		&job.AffectedBytes,
		&job.TempCatalogPath,
		&created,
		&started,
		&completed,
		&job.ErrorText,
	); err != nil {
		return job, mapNotFound(err)
	}
	job.Execute = execute != 0
	job.CreatedAt = parseTime(created)
	job.StartedAt = parseOptionalTime(started)
	job.CompletedAt = parseOptionalTime(completed)
	return job, nil
}

const maintenanceSelect = `SELECT id,source_id,kind,status,execute,hash_mib_per_second,EXISTS(SELECT 1 FROM repair_replacement_approvals ap JOIN repair_scans r ON r.job_id=ap.job_id JOIN sources src ON src.id=maintenance_jobs.source_id JOIN source_states st ON st.source_id=src.id WHERE ap.job_id=maintenance_jobs.id AND st.catalog_version=r.catalog_version AND src.updated_at=r.source_updated_at),scanned_objects,scanned_bytes,completed_projects,total_projects,last_project,candidate_objects,candidate_bytes,affected_objects,affected_bytes,temp_catalog_path,created_at,started_at,completed_at,error_text FROM maintenance_jobs`

func (s *ServerStore) CreateMaintenanceJob(ctx context.Context, job domain.MaintenanceJob) error {
	if job.ID == "" || job.SourceID == "" {
		return fmt.Errorf("maintenance job id/sourceId are required: %w", domain.ErrInvalid)
	}
	if job.Kind != domain.MaintenanceInventory && job.Kind != domain.MaintenanceGC && job.Kind != domain.MaintenanceRepair {
		return fmt.Errorf("unsupported maintenance kind %q: %w", job.Kind, domain.ErrInvalid)
	}
	if job.HashMiBPerSecond < 0 || job.HashMiBPerSecond > 1024 || (job.HashMiBPerSecond > 0 && job.Kind != domain.MaintenanceRepair) {
		return fmt.Errorf("invalid integrity check speed: %w", domain.ErrInvalid)
	}
	if job.Status != domain.MaintenanceQueued {
		return fmt.Errorf("new maintenance job must start QUEUED: %w", domain.ErrInvalid)
	}
	if job.CreatedAt.IsZero() {
		return fmt.Errorf("maintenance job createdAt is required: %w", domain.ErrInvalid)
	}
	execute := 0
	if job.Execute {
		execute = 1
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO maintenance_jobs(id,source_id,kind,status,execute,hash_mib_per_second,created_at) VALUES(?,?,?,?,?,?,?)`, job.ID, job.SourceID, job.Kind, job.Status, execute, job.HashMiBPerSecond, timeString(job.CreatedAt))
	if err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "unique constraint") {
			return fmt.Errorf("source already has an active maintenance job: %w", domain.ErrConflict)
		}
		if strings.Contains(lower, "source has active epoch; maintenance is blocked") {
			return fmt.Errorf("source has an active epoch; maintenance is blocked: %w", domain.ErrConflict)
		}
		return err
	}
	return nil
}

func (s *ServerStore) GetMaintenanceJob(ctx context.Context, id string) (domain.MaintenanceJob, error) {
	return scanMaintenanceJob(s.DB.QueryRowContext(ctx, maintenanceSelect+` WHERE id=?`, id))
}

func (s *ServerStore) ListMaintenanceJobs(ctx context.Context, sourceID string, limit int) ([]domain.MaintenanceJob, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query := maintenanceSelect
	args := make([]any, 0, 2)
	if sourceID != "" {
		query += ` WHERE source_id=?`
		args = append(args, sourceID)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.MaintenanceJob, 0)
	for rows.Next() {
		job, scanErr := scanMaintenanceJob(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (s *ServerStore) TransitionMaintenanceJob(ctx context.Context, id string, to domain.MaintenanceStatus, errorText string, at time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var from domain.MaintenanceStatus
	if err = tx.QueryRowContext(ctx, `SELECT status FROM maintenance_jobs WHERE id=?`, id).Scan(&from); err != nil {
		return mapNotFound(err)
	}
	if err = domain.ValidateMaintenanceTransition(from, to); err != nil {
		return err
	}
	started := any(nil)
	completed := any(nil)
	if to == domain.MaintenanceRunning {
		started = timeString(at)
	}
	if to == domain.MaintenanceCompleted || to == domain.MaintenanceFailed || to == domain.MaintenanceCancelled {
		completed = timeString(at)
	}
	result, err := tx.ExecContext(ctx, `UPDATE maintenance_jobs SET status=?,error_text=?,started_at=COALESCE(?,started_at),completed_at=COALESCE(?,completed_at) WHERE id=? AND status=?`, to, errorText, started, completed, id, from)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return fmt.Errorf("maintenance job status changed concurrently: %w", domain.ErrConflict)
	}
	return tx.Commit()
}

func (s *ServerStore) UpdateMaintenanceProgress(ctx context.Context, id string, p ports.MaintenanceProgress) error {
	if p.ScannedObjects < 0 || p.ScannedBytes < 0 || p.CandidateObjects < 0 || p.CandidateBytes < 0 || p.AffectedObjects < 0 || p.AffectedBytes < 0 {
		return fmt.Errorf("maintenance progress cannot be negative: %w", domain.ErrInvalid)
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE maintenance_jobs SET scanned_objects=?,scanned_bytes=?,candidate_objects=?,candidate_bytes=?,affected_objects=?,affected_bytes=? WHERE id=? AND status=?`, p.ScannedObjects, p.ScannedBytes, p.CandidateObjects, p.CandidateBytes, p.AffectedObjects, p.AffectedBytes, id, domain.MaintenanceRunning)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return fmt.Errorf("maintenance job is not running: %w", domain.ErrConflict)
	}
	return nil
}

func (s *ServerStore) SetMaintenanceCatalogPath(ctx context.Context, id, catalogPath string) error {
	if strings.TrimSpace(catalogPath) == "" {
		return fmt.Errorf("maintenance catalog path is required: %w", domain.ErrInvalid)
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE maintenance_jobs SET temp_catalog_path=? WHERE id=? AND status=?`, catalogPath, id, domain.MaintenanceRunning)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return fmt.Errorf("maintenance job is not running: %w", domain.ErrConflict)
	}
	return nil
}

var _ ports.MaintenanceStore = (*ServerStore)(nil)

func (s *ServerStore) UpdateInventoryCheckpoint(ctx context.Context, id string, p ports.MaintenanceProgress) error {
	if p.ScannedObjects < 0 || p.ScannedBytes < 0 || p.CompletedProjects < 0 || p.TotalProjects < 0 ||
		p.CompletedProjects > p.TotalProjects {
		return fmt.Errorf("invalid PyPI checkpoint counters: %w", domain.ErrInvalid)
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE maintenance_jobs
SET scanned_objects=?,scanned_bytes=?,completed_projects=?,total_projects=?,last_project=?
WHERE id=? AND kind=? AND status=?`, p.ScannedObjects, p.ScannedBytes,
		p.CompletedProjects, p.TotalProjects, p.LastProject, id, domain.MaintenanceInventory, domain.MaintenanceRunning)
	if err != nil {
		return err
	}
	if count, _ := res.RowsAffected(); count != 1 {
		return fmt.Errorf("PyPI inventory task is not running: %w", domain.ErrConflict)
	}
	return nil
}
