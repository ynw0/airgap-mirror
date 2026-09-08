package sqlite

import (
	"context"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type DownloadBatchStats struct {
	TotalBytes      int64
	DownloadedBytes int64
	TotalEntries    int64
	VerifiedEntries int64
	FailedEntries   int64
}

func (c *ClientStore) DownloadBatchStats(ctx context.Context, batchID string) (DownloadBatchStats, error) {
	var out DownloadBatchStats
	err := c.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(a.size),0),COALESCE(SUM(d.downloaded),0),COUNT(*),COALESCE(SUM(CASE WHEN d.status=? THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN d.status=? THEN 1 ELSE 0 END),0) FROM download_entries d JOIN planned_artifacts a ON a.id=d.artifact_id WHERE d.batch_id=?`, domain.DownloadVerified, domain.DownloadFailed, batchID).Scan(&out.TotalBytes, &out.DownloadedBytes, &out.TotalEntries, &out.VerifiedEntries, &out.FailedEntries)
	return out, err
}

func (c *ClientStore) ListEpochsForCapsule(ctx context.Context, capsuleID, sourceID string) ([]domain.Epoch, error) {
	query := `SELECT id,source_id,base_cursor_kind,base_cursor_value,target_cursor_kind,target_cursor_value,status,total_bytes,total_objects,total_batches,publish_unit_count,created_at,error_text FROM download_epochs WHERE capsule_id=?`
	args := []any{capsuleID}
	if sourceID != "" {
		query += ` AND source_id=?`
		args = append(args, sourceID)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := c.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Epoch
	for rows.Next() {
		var e domain.Epoch
		var baseKind, baseValue, targetKind, targetValue, created string
		if err = rows.Scan(&e.ID, &e.SourceID, &baseKind, &baseValue, &targetKind, &targetValue, &e.Status, &e.TotalBytes, &e.TotalObjects, &e.TotalBatches, &e.PublishUnitCount, &created, &e.ErrorText); err != nil {
			return nil, err
		}
		e.BaseCursor = domain.Cursor{Kind: baseKind, Value: baseValue}
		e.TargetCursor = domain.Cursor{Kind: targetKind, Value: targetValue}
		e.CreatedAt = parseTime(created)
		out = append(out, e)
	}
	return out, rows.Err()
}
