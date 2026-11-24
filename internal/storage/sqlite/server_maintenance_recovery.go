package sqlite

import (
	"context"
	"database/sql"
	"os"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

// RecoverInterruptedMaintenanceJobs leaves resumable PyPI jobs paused.
// Legacy temporary catalogs were not durable and must not be resumed.
func (s *ServerStore) RecoverInterruptedMaintenanceJobs(ctx context.Context, at time.Time, message string) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT j.id,j.kind,j.status,j.temp_catalog_path,COALESCE(s.type,''),COALESCE(s.provider,'')
FROM maintenance_jobs j LEFT JOIN sources s ON s.id=j.source_id
WHERE j.status IN(?,?)`, domain.MaintenanceQueued, domain.MaintenanceRunning)
	if err != nil {
		return err
	}
	type item struct {
		id                       string
		kind                     domain.MaintenanceKind
		status                   domain.MaintenanceStatus
		path, typeName, provider string
	}
	var items []item
	for rows.Next() {
		var it item
		if err = rows.Scan(&it.id, &it.kind, &it.status, &it.path, &it.typeName, &it.provider); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, it := range items {
		target := domain.MaintenanceFailed
		if it.typeName == "pypi" && it.provider == "pypi" &&
			(it.kind == domain.MaintenanceRepair || (it.kind == domain.MaintenanceInventory && hasProjectCheckpoint(ctx, it.path))) {
			target = domain.MaintenancePaused
		}
		completed := any(timeString(at))
		if target == domain.MaintenancePaused {
			completed = nil
		}
		if _, err = s.DB.ExecContext(ctx, `UPDATE maintenance_jobs SET status=?,error_text=?,completed_at=? WHERE id=? AND status IN(?,?)`,
			target, message, completed, it.id, domain.MaintenanceQueued, domain.MaintenanceRunning); err != nil {
			return err
		}
	}
	return nil
}

func hasProjectCheckpoint(ctx context.Context, path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return false
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var count int
	return db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='inventory_checkpoint'`).Scan(&count) == nil && count == 1
}
