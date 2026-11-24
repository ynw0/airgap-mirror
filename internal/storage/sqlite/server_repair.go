package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func (s *ServerStore) BeginRepairScan(ctx context.Context, jobID, sourceID string) (domain.RepairCheckpoint, error) {
	var cp domain.RepairCheckpoint
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return cp, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO repair_scans(job_id,catalog_version,source_updated_at)
SELECT j.id,st.catalog_version,src.updated_at FROM maintenance_jobs j JOIN source_states st ON st.source_id=j.source_id JOIN sources src ON src.id=j.source_id
WHERE j.id=? AND j.source_id=? AND j.kind='REPAIR' AND j.status='RUNNING' AND COALESCE(st.active_epoch_id,'')=''
ON CONFLICT(job_id) DO NOTHING`, jobID, sourceID); err != nil {
		return cp, err
	}
	err = tx.QueryRowContext(ctx, `SELECT r.last_path,r.catalog_version FROM repair_scans r JOIN maintenance_jobs j ON j.id=r.job_id JOIN sources src ON src.id=j.source_id JOIN source_states st ON st.source_id=j.source_id
WHERE r.job_id=? AND j.source_id=? AND j.status='RUNNING' AND st.catalog_version=r.catalog_version AND src.updated_at=r.source_updated_at AND COALESCE(st.active_epoch_id,'')=''`, jobID, sourceID).Scan(&cp.LastPath, &cp.CatalogVersion)
	if err != nil {
		return cp, fmt.Errorf("修复检查对应的软件源状态已经变化，请重新检查: %w", domain.ErrConflict)
	}
	return cp, tx.Commit()
}

func (s *ServerStore) RepairCatalogPage(ctx context.Context, sourceID, after string, limit int) ([]domain.CatalogEntry, error) {
	if limit < 1 || limit > 10000 {
		return nil, domain.ErrInvalid
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT logical_path,size,sha256,package_key FROM live_catalog_entries WHERE source_id=? AND logical_path>? ORDER BY logical_path LIMIT ?`, sourceID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.CatalogEntry, 0, limit)
	for rows.Next() {
		v := domain.CatalogEntry{SourceID: sourceID}
		if err = rows.Scan(&v.LogicalPath, &v.Size, &v.SHA256, &v.PackageKey); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *ServerStore) CommitRepairPage(ctx context.Context, jobID, previous, last string, count, bytes int64, issues []domain.RepairIssue) error {
	if count <= 0 || bytes < 0 || last <= previous || int64(len(issues)) > count {
		return domain.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE repair_scans SET last_path=? WHERE job_id=? AND last_path=? AND EXISTS(
 SELECT 1 FROM maintenance_jobs j JOIN sources src ON src.id=j.source_id JOIN source_states st ON st.source_id=j.source_id
 WHERE j.id=repair_scans.job_id AND j.status='RUNNING' AND st.catalog_version=repair_scans.catalog_version AND src.updated_at=repair_scans.source_updated_at AND COALESCE(st.active_epoch_id,'')='')`, last, jobID, previous)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrConflict
	}
	var issueBytes int64
	for _, v := range issues {
		if v.LogicalPath <= previous || v.LogicalPath > last || v.Size < 0 {
			return domain.ErrInvalid
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO repair_issues(job_id,logical_path,package_key,size,sha256,kind,actual_size) VALUES(?,?,?,?,?,?,?)`, jobID, v.LogicalPath, v.PackageKey, v.Size, v.SHA256, v.Kind, v.ActualSize); err != nil {
			return err
		}
		issueBytes += v.Size
	}
	res, err = tx.ExecContext(ctx, `UPDATE maintenance_jobs SET scanned_objects=scanned_objects+?,scanned_bytes=scanned_bytes+?,candidate_objects=candidate_objects+?,candidate_bytes=candidate_bytes+? WHERE id=? AND status='RUNNING'`, count, bytes, len(issues), issueBytes, jobID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrConflict
	}
	return tx.Commit()
}

func (s *ServerStore) FinishRepairScan(ctx context.Context, jobID string, at time.Time) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE maintenance_jobs SET status='COMPLETED',completed_at=?,error_text='' WHERE id=? AND status='RUNNING' AND EXISTS(
 SELECT 1 FROM repair_scans r JOIN source_states st ON st.source_id=maintenance_jobs.source_id JOIN sources src ON src.id=st.source_id
 WHERE r.job_id=maintenance_jobs.id AND r.catalog_version=st.catalog_version AND r.source_updated_at=src.updated_at AND COALESCE(st.active_epoch_id,'')='')`, timeString(at), jobID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (s *ServerStore) ListRepairIssues(ctx context.Context, jobID, after string, limit int) ([]domain.RepairIssue, error) {
	if limit < 1 || limit > 1000 {
		return nil, domain.ErrInvalid
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT logical_path,package_key,size,sha256,kind,actual_size FROM repair_issues WHERE job_id=? AND logical_path>? ORDER BY logical_path LIMIT ?`, jobID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.RepairIssue, 0, limit)
	for rows.Next() {
		var v domain.RepairIssue
		if err = rows.Scan(&v.LogicalPath, &v.PackageKey, &v.Size, &v.SHA256, &v.Kind, &v.ActualSize); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
