package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func (s *ServerStore) CountImportedBatches(ctx context.Context, epochID string) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM batches WHERE epoch_id=? AND status=?`, epochID, domain.BatchImported).Scan(&n)
	return n, err
}
func (s *ServerStore) CountPublishedUnits(ctx context.Context, epochID string) (int64, error) {
	var n int64
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM publish_units WHERE epoch_id=? AND status=?`, epochID, domain.PublishPublished).Scan(&n)
	return n, err
}
func (s *ServerStore) CompleteEpoch(ctx context.Context, epochID string, stats domain.CatalogStats, at time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sourceID, kind, value string
	var status domain.EpochStatus
	var batches int
	var units int64
	err = tx.QueryRowContext(ctx, `SELECT source_id,target_cursor_kind,target_cursor_value,status,total_batches,publish_unit_count FROM epochs WHERE id=?`, epochID).Scan(&sourceID, &kind, &value, &status, &batches, &units)
	if err != nil {
		return mapNotFound(err)
	}
	if status != domain.EpochPublished {
		return fmt.Errorf("epoch %s cannot complete from %s: %w", epochID, status, domain.ErrConflict)
	}
	var imported int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM batches WHERE epoch_id=? AND status=?`, epochID, domain.BatchImported).Scan(&imported); err != nil {
		return err
	}
	var published int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publish_units WHERE epoch_id=? AND status=?`, epochID, domain.PublishPublished).Scan(&published); err != nil {
		return err
	}
	if imported != batches || published != units {
		return fmt.Errorf("epoch incomplete batches=%d/%d units=%d/%d: %w", imported, batches, published, units, domain.ErrConflict)
	}
	_, err = tx.ExecContext(ctx, `UPDATE epochs SET status=?,completed_at=?,error_text='' WHERE id=?`, domain.EpochComplete, timeString(at), epochID)
	if err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `UPDATE source_states SET cursor_kind=?,cursor_value=?,catalog_version=catalog_version+1,live_bytes=?,live_objects=?,active_epoch_id=NULL,updated_at=? WHERE source_id=? AND active_epoch_id=?`, kind, value, stats.Bytes, stats.Objects, timeString(at), sourceID, epochID)
	if err != nil {
		return err
	}
	affected, _ := r.RowsAffected()
	if affected != 1 {
		return fmt.Errorf("source active epoch changed before cursor advance: %w", domain.ErrConflict)
	}
	return tx.Commit()
}

// ExportSource writes a minimal per-source catalog SQLite file without scanning repository files.
