package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func (s *ServerStore) ListImportSessionsByEpoch(ctx context.Context, epochID string) ([]domain.ImportSession, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,request_id,source_id,epoch_id,batch_id,manifest_path,manifest_expected_size,manifest_expected_sha256,manifest_uploaded_size,status,staging_path,created_at,updated_at,error_text FROM import_sessions WHERE epoch_id=? ORDER BY created_at`, epochID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ImportSession
	for rows.Next() {
		v, e := scanImportSession(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *ServerStore) CancelEpoch(ctx context.Context, epochID string, at time.Time, message string) error {
	if epochID == "" {
		return fmt.Errorf("epoch id is required: %w", domain.ErrInvalid)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sourceID string
	var status domain.EpochStatus
	err = tx.QueryRowContext(ctx, `SELECT source_id,status FROM epochs WHERE id=?`, epochID).Scan(&sourceID, &status)
	if err != nil {
		return mapNotFound(err)
	}
	if status != domain.EpochCancelled {
		if err = domain.ValidateEpochTransition(status, domain.EpochCancelled); err != nil {
			return fmt.Errorf("%s: %w", err.Error(), domain.ErrConflict)
		}
		var imported int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM imported_entries WHERE epoch_id=? LIMIT 1)`, epochID).Scan(&imported); err != nil {
			return err
		}
		if imported != 0 {
			return fmt.Errorf("已有文件导入服务器，不能直接取消；请继续完成本次导入: %w", domain.ErrConflict)
		}
		// IMPORTING is persisted before the first repository write. A crash
		// between filesystem installation and entry commit must not release
		// the source reservation merely because imported_entries is empty.
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM batches WHERE epoch_id=? AND status IN('IMPORTING','IMPORTED'))`, epochID).Scan(&imported); err != nil {
			return err
		}
		if imported != 0 {
			return fmt.Errorf("已进入文件安装阶段，可能已有文件写入仓库，不能直接取消；请继续完成本次导入: %w", domain.ErrConflict)
		}
		_, err = tx.ExecContext(ctx, `UPDATE epochs SET status=?,error_text=? WHERE id=?`, domain.EpochCancelled, message, epochID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE batches SET status=?,error_text=? WHERE epoch_id=? AND status<>?`, domain.BatchFailed, message, epochID, domain.BatchImported)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE packs SET status=? WHERE epoch_id=? AND status<>?`, domain.PackFailed, epochID, domain.PackImported)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE import_sessions SET status=?,error_text=?,updated_at=? WHERE epoch_id=? AND status<>? AND status<>?`, "CANCELLED", message, timeString(at), epochID, "BATCH_IMPORTED", "CANCELLED")
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE import_packs SET status=? WHERE session_id IN (SELECT id FROM import_sessions WHERE epoch_id=?) AND status<>?`, domain.PackFailed, epochID, domain.PackImported)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE source_states SET active_epoch_id=NULL,updated_at=? WHERE source_id=? AND active_epoch_id=?`, timeString(at), sourceID, epochID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
