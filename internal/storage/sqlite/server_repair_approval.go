package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

// ApproveRepairReplacement authorizes only the immutable package files in a
// completed full integrity report, not arbitrary replacement of repository data.
// Approval does not modify the filesystem or live Catalog.
func (s *ServerStore) ApproveRepairReplacement(ctx context.Context, jobID string, approve bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sourceID string
	var active string
	var busy int
	err = tx.QueryRowContext(ctx, `SELECT j.source_id,COALESCE(st.active_epoch_id,''),EXISTS(
 SELECT 1 FROM maintenance_jobs m WHERE m.source_id=j.source_id AND m.status IN('QUEUED','RUNNING','PAUSED'))
 FROM maintenance_jobs j JOIN source_states st ON st.source_id=j.source_id
 WHERE j.id=? AND j.kind='REPAIR'`, jobID).Scan(&sourceID, &active, &busy)
	if err != nil {
		return mapNotFound(err)
	}
	if active != "" || busy != 0 {
		return fmt.Errorf("软件源正在同步或维护: %w", domain.ErrConflict)
	}
	if !approve {
		if _, err = tx.ExecContext(ctx, "DELETE FROM repair_replacement_approvals WHERE job_id=?", jobID); err != nil {
			return err
		}
		return tx.Commit()
	}
	var eligible int
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM repair_scans r JOIN maintenance_jobs j ON j.id=r.job_id
 JOIN sources src ON src.id=j.source_id JOIN source_states st ON st.source_id=src.id
 WHERE j.id=? AND j.status='COMPLETED' AND j.kind='REPAIR' AND j.hash_mib_per_second>0
 AND src.type='pypi' AND src.provider='pypi'
 AND st.catalog_version=r.catalog_version AND src.updated_at=r.source_updated_at
 AND EXISTS(SELECT 1 FROM repair_issues i JOIN live_catalog_entries c
 ON c.source_id=j.source_id AND c.logical_path=i.logical_path AND c.sha256=i.sha256 AND c.size=i.size
 WHERE i.job_id=j.id AND i.kind IN('SIZE_MISMATCH','HASH_MISMATCH') AND i.logical_path LIKE 'packages/%'
 AND length(i.actual_sha256)=64 AND i.actual_size>=0))`, jobID).Scan(&eligible)
	if err != nil {
		return err
	}
	if eligible != 1 {
		return fmt.Errorf("报告已失效，或没有经过 SHA256 校验的损坏软件包: %w", domain.ErrConflict)
	}
	// Only one report per source is approved at a time.
	if _, err = tx.ExecContext(ctx, `DELETE FROM repair_replacement_approvals WHERE job_id IN(SELECT id FROM maintenance_jobs WHERE source_id=?)`, sourceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO repair_replacement_approvals(job_id,approved_at) VALUES(?,?)`, jobID, timeString(time.Now())); err != nil {
		return err
	}
	return tx.Commit()
}

// ApprovedRepairEvidence revalidates the original report against the current
// live Catalog and active epoch. An offline manifest alone grants no authority.
func (s *ServerStore) ApprovedRepairEvidence(ctx context.Context, sourceID, epochID, logicalPath string, size int64, sha string) (domain.RepairReplacementEvidence, error) {
	var v domain.RepairReplacementEvidence
	err := s.DB.QueryRowContext(ctx, `SELECT j.id,i.logical_path,i.size,i.sha256,i.actual_size,i.actual_sha256
 FROM repair_replacement_approvals ap JOIN maintenance_jobs j ON j.id=ap.job_id
 JOIN repair_scans r ON r.job_id=j.id JOIN repair_issues i ON i.job_id=j.id
 JOIN sources src ON src.id=j.source_id JOIN source_states st ON st.source_id=src.id
 JOIN live_catalog_entries c ON c.source_id=src.id AND c.logical_path=i.logical_path AND c.sha256=i.sha256 AND c.size=i.size
 WHERE j.source_id=? AND st.active_epoch_id=? AND j.kind='REPAIR' AND j.status='COMPLETED' AND j.hash_mib_per_second>0
 AND src.type='pypi' AND src.provider='pypi' AND st.catalog_version=r.catalog_version AND src.updated_at=r.source_updated_at
 AND i.logical_path=? AND i.logical_path LIKE 'packages/%' AND i.size=? AND i.sha256=?
 AND i.kind IN('SIZE_MISMATCH','HASH_MISMATCH') AND length(i.actual_sha256)=64 AND i.actual_size>=0
 ORDER BY ap.approved_at DESC LIMIT 1`, sourceID, epochID, logicalPath, size, sha).Scan(&v.JobID, &v.LogicalPath, &v.Size, &v.SHA256, &v.ActualSize, &v.ActualSHA256)
	return v, mapNotFound(err)
}

func (s *ServerStore) HasApprovedRepair(ctx context.Context, sourceID, epochID string) (bool, error) {
	var found bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM repair_replacement_approvals ap
 JOIN maintenance_jobs j ON j.id=ap.job_id JOIN repair_scans r ON r.job_id=j.id
 JOIN sources src ON src.id=j.source_id JOIN source_states st ON st.source_id=src.id
 WHERE src.id=? AND st.active_epoch_id=? AND st.catalog_version=r.catalog_version AND src.updated_at=r.source_updated_at
 AND j.kind='REPAIR' AND j.status='COMPLETED' AND j.hash_mib_per_second>0 AND src.type='pypi' AND src.provider='pypi')`, sourceID, epochID).Scan(&found)
	return found, err
}
