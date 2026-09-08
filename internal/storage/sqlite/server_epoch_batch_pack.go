package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func (s *ServerStore) CreateEpoch(ctx context.Context, v domain.Epoch) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO epochs(id,source_id,base_cursor_kind,base_cursor_value,target_cursor_kind,target_cursor_value,status,total_bytes,total_objects,total_batches,publish_unit_count,created_at,error_text) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.SourceID, v.BaseCursor.Kind, v.BaseCursor.Value, v.TargetCursor.Kind, v.TargetCursor.Value, v.Status, v.TotalBytes, v.TotalObjects, v.TotalBatches, v.PublishUnitCount, timeString(v.CreatedAt), v.ErrorText)
	return err
}
func scanEpoch(row interface{ Scan(...any) error }) (domain.Epoch, error) {
	var v domain.Epoch
	var bk, bv, tk, tv, created string
	var completed sql.NullString
	err := row.Scan(&v.ID, &v.SourceID, &bk, &bv, &tk, &tv, &v.Status, &v.TotalBytes, &v.TotalObjects, &v.TotalBatches, &v.PublishUnitCount, &created, &completed, &v.ErrorText)
	if err != nil {
		return v, mapNotFound(err)
	}
	v.BaseCursor = domain.Cursor{Kind: bk, Value: bv}
	v.TargetCursor = domain.Cursor{Kind: tk, Value: tv}
	v.CreatedAt = parseTime(created)
	v.CompletedAt = parseOptionalTime(completed)
	return v, nil
}
func (s *ServerStore) GetEpoch(ctx context.Context, id string) (domain.Epoch, error) {
	return scanEpoch(s.DB.QueryRowContext(ctx, `SELECT id,source_id,base_cursor_kind,base_cursor_value,target_cursor_kind,target_cursor_value,status,total_bytes,total_objects,total_batches,publish_unit_count,created_at,completed_at,error_text FROM epochs WHERE id=?`, id))
}
func (s *ServerStore) ListEpochs(ctx context.Context, sourceID string) ([]domain.Epoch, error) {
	q := `SELECT id,source_id,base_cursor_kind,base_cursor_value,target_cursor_kind,target_cursor_value,status,total_bytes,total_objects,total_batches,publish_unit_count,created_at,completed_at,error_text FROM epochs`
	args := []any{}
	if sourceID != "" {
		q += ` WHERE source_id=?`
		args = append(args, sourceID)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Epoch
	for rows.Next() {
		v, e := scanEpoch(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *ServerStore) TransitionEpoch(ctx context.Context, id string, to domain.EpochStatus, msg string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var from domain.EpochStatus
	if err = tx.QueryRowContext(ctx, `SELECT status FROM epochs WHERE id=?`, id).Scan(&from); err != nil {
		return mapNotFound(err)
	}
	if err = domain.ValidateEpochTransition(from, to); err != nil {
		return err
	}
	var completed any = nil
	if to == domain.EpochComplete {
		completed = timeString(time.Now())
	}
	_, err = tx.ExecContext(ctx, `UPDATE epochs SET status=?,completed_at=COALESCE(?,completed_at),error_text=? WHERE id=?`, to, completed, msg, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ServerStore) CreateBatch(ctx context.Context, v domain.Batch) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO batches(id,epoch_id,source_id,sequence,status,planned_bytes,object_count,pack_count,created_at,error_text) VALUES(?,?,?,?,?,?,?,?,?,?)`, v.ID, v.EpochID, v.SourceID, v.Sequence, v.Status, v.PlannedBytes, v.ObjectCount, v.PackCount, timeString(v.CreatedAt), v.ErrorText)
	return err
}
func scanBatch(row interface{ Scan(...any) error }) (domain.Batch, error) {
	var v domain.Batch
	var created string
	var imported sql.NullString
	err := row.Scan(&v.ID, &v.EpochID, &v.SourceID, &v.Sequence, &v.Status, &v.PlannedBytes, &v.ObjectCount, &v.PackCount, &created, &imported, &v.ErrorText)
	if err != nil {
		return v, mapNotFound(err)
	}
	v.CreatedAt = parseTime(created)
	v.ImportedAt = parseOptionalTime(imported)
	return v, nil
}
func (s *ServerStore) GetBatch(ctx context.Context, id string) (domain.Batch, error) {
	return scanBatch(s.DB.QueryRowContext(ctx, `SELECT id,epoch_id,source_id,sequence,status,planned_bytes,object_count,pack_count,created_at,imported_at,error_text FROM batches WHERE id=?`, id))
}
func (s *ServerStore) ListBatches(ctx context.Context, epochID string) ([]domain.Batch, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,epoch_id,source_id,sequence,status,planned_bytes,object_count,pack_count,created_at,imported_at,error_text FROM batches WHERE epoch_id=? ORDER BY sequence`, epochID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Batch
	for rows.Next() {
		v, e := scanBatch(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *ServerStore) TransitionBatch(ctx context.Context, id string, to domain.BatchStatus, msg string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var from domain.BatchStatus
	if err = tx.QueryRowContext(ctx, `SELECT status FROM batches WHERE id=?`, id).Scan(&from); err != nil {
		return mapNotFound(err)
	}
	if err = domain.ValidateBatchTransition(from, to); err != nil {
		return err
	}
	var imported any = nil
	if to == domain.BatchImported {
		imported = timeString(time.Now())
	}
	_, err = tx.ExecContext(ctx, `UPDATE batches SET status=?,imported_at=COALESCE(?,imported_at),error_text=? WHERE id=?`, to, imported, msg, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ServerStore) UpsertPack(ctx context.Context, p domain.Pack) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO packs(id,epoch_id,batch_id,sequence,size,sha256,entry_count,uploaded_size,file_path,status) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET uploaded_size=excluded.uploaded_size,file_path=excluded.file_path,status=excluded.status`, p.ID, p.EpochID, p.BatchID, p.Sequence, p.Size, p.SHA256, p.EntryCount, p.UploadedSize, p.FilePath, p.Status)
	return err
}
func scanPack(row interface{ Scan(...any) error }) (domain.Pack, error) {
	var p domain.Pack
	err := row.Scan(&p.ID, &p.EpochID, &p.BatchID, &p.Sequence, &p.Size, &p.SHA256, &p.EntryCount, &p.UploadedSize, &p.FilePath, &p.Status)
	if err != nil {
		return p, mapNotFound(err)
	}
	return p, nil
}
func (s *ServerStore) GetPack(ctx context.Context, id string) (domain.Pack, error) {
	return scanPack(s.DB.QueryRowContext(ctx, `SELECT id,epoch_id,batch_id,sequence,size,sha256,entry_count,uploaded_size,file_path,status FROM packs WHERE id=?`, id))
}
func (s *ServerStore) ListPacks(ctx context.Context, batchID string) ([]domain.Pack, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,epoch_id,batch_id,sequence,size,sha256,entry_count,uploaded_size,file_path,status FROM packs WHERE batch_id=? ORDER BY sequence`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Pack
	for rows.Next() {
		v, e := scanPack(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
