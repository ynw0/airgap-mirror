package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func sameEpoch(e domain.Epoch, d domain.BatchDescriptor) bool {
	return e.ID == d.EpochID && e.SourceID == d.SourceID && e.BaseCursor.Equal(d.BaseCursor) && e.TargetCursor.Equal(d.TargetCursor) && e.TotalBytes == d.EpochTotalBytes && e.TotalObjects == d.EpochTotalObjects && e.TotalBatches == d.EpochTotalBatches && e.PublishUnitCount == d.EpochPublishUnitCount
}
func sameBatch(b domain.Batch, d domain.BatchDescriptor) bool {
	return b.ID == d.BatchID && b.EpochID == d.EpochID && b.SourceID == d.SourceID && b.Sequence == d.BatchSequence && b.PlannedBytes == d.BatchBytes && b.ObjectCount == d.BatchObjects && b.PackCount == len(d.Packs)
}

// FindExistingImportBundle checks an existing request before capacity preflight.
// It never creates directories, sessions, packs or changes the source state.
func (s *ServerStore) FindExistingImportBundle(ctx context.Context, requestID string, d domain.BatchDescriptor) (domain.ImportSession, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.ImportSession{}, err
	}
	defer tx.Rollback()
	return findExistingImportBundle(ctx, tx, requestID, d)
}

func findExistingImportBundle(ctx context.Context, tx *sql.Tx, requestID string, d domain.BatchDescriptor) (domain.ImportSession, error) {
	var empty domain.ImportSession
	rows, err := tx.QueryContext(ctx, `SELECT id,request_id,source_id,epoch_id,batch_id,manifest_path,manifest_expected_size,manifest_expected_sha256,manifest_uploaded_size,status,staging_path,created_at,updated_at,error_text FROM import_sessions WHERE request_id=? OR batch_id=? LIMIT 2`, requestID, d.BatchID)
	if err != nil {
		return empty, err
	}
	var matches []domain.ImportSession
	for rows.Next() {
		session, e := scanImportSession(rows)
		if e != nil {
			rows.Close()
			return empty, e
		}
		matches = append(matches, session)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return empty, err
	}
	if err = rows.Close(); err != nil {
		return empty, err
	}
	if len(matches) == 0 {
		return empty, domain.ErrNotFound
	}
	if len(matches) != 1 {
		return empty, fmt.Errorf("存在多个相关导入会话，请先核对历史会话，不能自动恢复: %w", domain.ErrConflict)
	}
	out := matches[0]
	if out.RequestID != requestID {
		return empty, fmt.Errorf("该批次已有导入会话，请使用原请求编号继续: %w", domain.ErrConflict)
	}
	if out.SourceID != d.SourceID || out.EpochID != d.EpochID || out.BatchID != d.BatchID || out.ManifestSize != d.ManifestSize || out.ManifestSHA256 != d.ManifestSHA256 {
		return empty, fmt.Errorf("request id reused for another bundle: %w", domain.ErrConflict)
	}
	epoch, err := scanEpoch(tx.QueryRowContext(ctx, `SELECT id,source_id,base_cursor_kind,base_cursor_value,target_cursor_kind,target_cursor_value,status,total_bytes,total_objects,total_batches,publish_unit_count,created_at,completed_at,error_text FROM epochs WHERE id=?`, d.EpochID))
	if err != nil {
		return empty, fmt.Errorf("existing import epoch unavailable: %v: %w", err, domain.ErrConflict)
	}
	batch, err := scanBatch(tx.QueryRowContext(ctx, `SELECT id,epoch_id,source_id,sequence,status,planned_bytes,object_count,pack_count,created_at,imported_at,error_text FROM batches WHERE id=?`, d.BatchID))
	if err != nil {
		return empty, fmt.Errorf("existing import batch unavailable: %v: %w", err, domain.ErrConflict)
	}
	if !sameEpoch(epoch, d) || !sameBatch(batch, d) || epoch.Status == domain.EpochCancelled || epoch.Status == domain.EpochFailed || out.Status == "CANCELLED" {
		return empty, fmt.Errorf("existing import bundle identity or status mismatch: %w", domain.ErrConflict)
	}
	if epoch.Status == domain.EpochComplete && (out.Status != "BATCH_IMPORTED" || batch.Status != domain.BatchImported) {
		return empty, fmt.Errorf("completed epoch has incomplete import session: %w", domain.ErrConflict)
	}
	var stateKind, stateValue, active string
	if err = tx.QueryRowContext(ctx, `SELECT cursor_kind,cursor_value,COALESCE(active_epoch_id,'') FROM source_states WHERE source_id=?`, d.SourceID).Scan(&stateKind, &stateValue, &active); err != nil {
		return empty, fmt.Errorf("existing import source state unavailable: %v: %w", err, domain.ErrConflict)
	}
	live := domain.Cursor{Kind: stateKind, Value: stateValue}
	if epoch.Status == domain.EpochComplete {
		if active != "" || !live.Equal(d.TargetCursor) {
			return empty, fmt.Errorf("source has advanced beyond completed import: %w", domain.ErrConflict)
		}
	} else if active != d.EpochID || !live.Equal(d.BaseCursor) {
		return empty, fmt.Errorf("existing import no longer holds the source reservation: %w", domain.ErrConflict)
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM packs WHERE batch_id=?`, d.BatchID).Scan(&count); err != nil {
		return empty, err
	}
	if count != len(d.Packs) {
		return empty, fmt.Errorf("existing pack count mismatch: %w", domain.ErrConflict)
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM import_packs WHERE session_id=?`, out.ID).Scan(&count); err != nil {
		return empty, err
	}
	if count != len(d.Packs) {
		return empty, fmt.Errorf("existing session pack count mismatch: %w", domain.ErrConflict)
	}
	seen := make(map[string]struct{}, len(d.Packs))
	for _, spec := range d.Packs {
		if _, duplicate := seen[spec.ID]; duplicate {
			return empty, domain.ErrInvalid
		}
		seen[spec.ID] = struct{}{}
		p, e := scanPack(tx.QueryRowContext(ctx, `SELECT id,epoch_id,batch_id,sequence,size,sha256,entry_count,uploaded_size,file_path,status FROM packs WHERE id=?`, spec.ID))
		if e != nil || p.EpochID != d.EpochID || p.BatchID != d.BatchID || p.Sequence != spec.Sequence || p.Size != spec.Size || p.SHA256 != spec.SHA256 || p.EntryCount != spec.EntryCount {
			return empty, fmt.Errorf("existing pack declaration mismatch: %w", domain.ErrConflict)
		}
		ip, e := scanImportPack(tx.QueryRowContext(ctx, `SELECT session_id,pack_id,expected_size,expected_sha256,uploaded_size,staging_path,status FROM import_packs WHERE session_id=? AND pack_id=?`, out.ID, spec.ID))
		if e != nil || ip.ExpectedSize != spec.Size || ip.ExpectedSHA256 != spec.SHA256 {
			return empty, fmt.Errorf("existing session pack declaration mismatch: %w", domain.ErrConflict)
		}
		if out.Status == "BATCH_IMPORTED" && (ip.Status != domain.PackImported || p.Status != domain.PackImported) {
			return empty, fmt.Errorf("imported batch has uncommitted pack: %w", domain.ErrConflict)
		}
	}
	return out, nil
}

func (s *ServerStore) RegisterImportBundle(ctx context.Context, d domain.BatchDescriptor, session domain.ImportSession, packs []domain.ImportPack) (domain.ImportSession, error) {
	if d.SchemaVersion != domain.BundleSchemaVersion {
		return domain.ImportSession{}, fmt.Errorf("unsupported bundle schema %d: %w", d.SchemaVersion, domain.ErrInvalid)
	}
	if d.SourceID == "" || d.EpochID == "" || d.BatchID == "" || d.EpochTotalBatches <= 0 || d.EpochPublishUnitCount < 0 {
		return domain.ImportSession{}, domain.ErrInvalid
	}
	if len(d.Packs) != len(packs) {
		return domain.ImportSession{}, fmt.Errorf("pack declaration mismatch: %w", domain.ErrInvalid)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.ImportSession{}, err
	}
	defer tx.Rollback()
	if existing, lookupErr := findExistingImportBundle(ctx, tx, session.RequestID, d); lookupErr == nil {
		return existing, tx.Commit()
	} else if !errors.Is(lookupErr, domain.ErrNotFound) {
		return domain.ImportSession{}, lookupErr
	}
	var stateKind, stateValue string
	var active sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT cursor_kind,cursor_value,active_epoch_id FROM source_states WHERE source_id=?`, d.SourceID).Scan(&stateKind, &stateValue, &active); err != nil {
		return domain.ImportSession{}, mapNotFound(err)
	}
	live := domain.Cursor{Kind: stateKind, Value: stateValue}
	if !live.Equal(d.BaseCursor) {
		return domain.ImportSession{}, fmt.Errorf("server cursor %v does not equal bundle base %v: %w", live, d.BaseCursor, domain.ErrConflict)
	}
	if active.Valid && active.String != "" && active.String != d.EpochID {
		return domain.ImportSession{}, fmt.Errorf("source has active epoch %s: %w", active.String, domain.ErrConflict)
	}

	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM epochs WHERE id=?`, d.EpochID).Scan(&exists); err != nil {
		return domain.ImportSession{}, err
	}
	if exists == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO epochs(id,source_id,base_cursor_kind,base_cursor_value,target_cursor_kind,target_cursor_value,status,total_bytes,total_objects,total_batches,publish_unit_count,created_at,error_text) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, d.EpochID, d.SourceID, d.BaseCursor.Kind, d.BaseCursor.Value, d.TargetCursor.Kind, d.TargetCursor.Value, domain.EpochTransferring, d.EpochTotalBytes, d.EpochTotalObjects, d.EpochTotalBatches, d.EpochPublishUnitCount, timeString(d.CreatedAt), "")
		if err != nil {
			return domain.ImportSession{}, err
		}
	} else {
		var e domain.Epoch
		var bk, bv, tk, tv, created string
		var completed sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT id,source_id,base_cursor_kind,base_cursor_value,target_cursor_kind,target_cursor_value,status,total_bytes,total_objects,total_batches,publish_unit_count,created_at,completed_at,error_text FROM epochs WHERE id=?`, d.EpochID).Scan(&e.ID, &e.SourceID, &bk, &bv, &tk, &tv, &e.Status, &e.TotalBytes, &e.TotalObjects, &e.TotalBatches, &e.PublishUnitCount, &created, &completed, &e.ErrorText)
		if err != nil {
			return domain.ImportSession{}, err
		}
		e.BaseCursor = domain.Cursor{Kind: bk, Value: bv}
		e.TargetCursor = domain.Cursor{Kind: tk, Value: tv}
		if e.Status == domain.EpochCancelled || e.Status == domain.EpochFailed || e.Status == domain.EpochComplete {
			return domain.ImportSession{}, fmt.Errorf("epoch %s is %s: %w", e.ID, e.Status, domain.ErrConflict)
		}
		if !sameEpoch(e, d) {
			return domain.ImportSession{}, fmt.Errorf("epoch immutable fields mismatch: %w", domain.ErrConflict)
		}
	}

	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM batches WHERE id=?`, d.BatchID).Scan(&exists); err != nil {
		return domain.ImportSession{}, err
	}
	if exists == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO batches(id,epoch_id,source_id,sequence,status,planned_bytes,object_count,pack_count,created_at,error_text) VALUES(?,?,?,?,?,?,?,?,?,?)`, d.BatchID, d.EpochID, d.SourceID, d.BatchSequence, domain.BatchTransferring, d.BatchBytes, d.BatchObjects, len(d.Packs), timeString(d.CreatedAt), "")
		if err != nil {
			return domain.ImportSession{}, err
		}
	} else {
		var b domain.Batch
		var created string
		var imported sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT id,epoch_id,source_id,sequence,status,planned_bytes,object_count,pack_count,created_at,imported_at,error_text FROM batches WHERE id=?`, d.BatchID).Scan(&b.ID, &b.EpochID, &b.SourceID, &b.Sequence, &b.Status, &b.PlannedBytes, &b.ObjectCount, &b.PackCount, &created, &imported, &b.ErrorText)
		if err != nil {
			return domain.ImportSession{}, err
		}
		if !sameBatch(b, d) {
			return domain.ImportSession{}, fmt.Errorf("batch immutable fields mismatch: %w", domain.ErrConflict)
		}
	}

	for _, spec := range d.Packs {
		var eid, bid, sha string
		var seq int
		var size, count int64
		err = tx.QueryRowContext(ctx, `SELECT epoch_id,batch_id,sequence,size,sha256,entry_count FROM packs WHERE id=?`, spec.ID).Scan(&eid, &bid, &seq, &size, &sha, &count)
		if err == nil {
			if eid != d.EpochID || bid != d.BatchID || seq != spec.Sequence || size != spec.Size || sha != spec.SHA256 || count != spec.EntryCount {
				return domain.ImportSession{}, fmt.Errorf("pack %s immutable fields mismatch: %w", spec.ID, domain.ErrConflict)
			}
		} else if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, `INSERT INTO packs(id,epoch_id,batch_id,sequence,size,sha256,entry_count,uploaded_size,file_path,status) VALUES(?,?,?,?,?,?,?,0,'',?)`, spec.ID, d.EpochID, d.BatchID, spec.Sequence, spec.Size, spec.SHA256, spec.EntryCount, domain.PackUploading)
			if err != nil {
				return domain.ImportSession{}, err
			}
		} else {
			return domain.ImportSession{}, err
		}
	}


	_, err = tx.ExecContext(ctx, `INSERT INTO import_sessions(id,request_id,source_id,epoch_id,batch_id,manifest_expected_size,manifest_expected_sha256,manifest_uploaded_size,manifest_path,staging_path,status,created_at,updated_at,error_text) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, session.ID, session.RequestID, d.SourceID, d.EpochID, d.BatchID, d.ManifestSize, d.ManifestSHA256, session.ManifestUploaded, session.ManifestPath, session.StagingPath, session.Status, timeString(session.CreatedAt), timeString(session.UpdatedAt), session.ErrorText)
	if err != nil {
		return domain.ImportSession{}, err
	}
	for _, p := range packs {
		_, err = tx.ExecContext(ctx, `INSERT INTO import_packs(session_id,pack_id,expected_size,expected_sha256,uploaded_size,staging_path,status) VALUES(?,?,?,?,?,?,?)`, session.ID, p.PackID, p.ExpectedSize, p.ExpectedSHA256, p.UploadedSize, p.StagingPath, p.Status)
		if err != nil {
			return domain.ImportSession{}, err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE source_states SET active_epoch_id=?,updated_at=? WHERE source_id=?`, d.EpochID, timeString(session.UpdatedAt), d.SourceID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "source has active maintenance job; epoch is blocked") {
			return domain.ImportSession{}, fmt.Errorf("source has an active maintenance job; import is blocked: %w", domain.ErrConflict)
		}
		return domain.ImportSession{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.ImportSession{}, err
	}
	return session, nil
}

func scanImportSession(row interface{ Scan(...any) error }) (domain.ImportSession, error) {
	var v domain.ImportSession
	var created, updated string
	err := row.Scan(&v.ID, &v.RequestID, &v.SourceID, &v.EpochID, &v.BatchID, &v.ManifestPath, &v.ManifestSize, &v.ManifestSHA256, &v.ManifestUploaded, &v.Status, &v.StagingPath, &created, &updated, &v.ErrorText)
	if err != nil {
		return v, mapNotFound(err)
	}
	v.CreatedAt = parseTime(created)
	v.UpdatedAt = parseTime(updated)
	return v, nil
}
func (s *ServerStore) GetImportSession(ctx context.Context, id string) (domain.ImportSession, error) {
	return scanImportSession(s.DB.QueryRowContext(ctx, `SELECT id,request_id,source_id,epoch_id,batch_id,manifest_path,manifest_expected_size,manifest_expected_sha256,manifest_uploaded_size,status,staging_path,created_at,updated_at,error_text FROM import_sessions WHERE id=?`, id))
}
func (s *ServerStore) GetImportSessionByRequest(ctx context.Context, id string) (domain.ImportSession, error) {
	return scanImportSession(s.DB.QueryRowContext(ctx, `SELECT id,request_id,source_id,epoch_id,batch_id,manifest_path,manifest_expected_size,manifest_expected_sha256,manifest_uploaded_size,status,staging_path,created_at,updated_at,error_text FROM import_sessions WHERE request_id=?`, id))
}
func (s *ServerStore) UpdateImportSession(ctx context.Context, v domain.ImportSession) error {
	r, err := s.DB.ExecContext(ctx, `UPDATE import_sessions SET manifest_uploaded_size=?,status=?,updated_at=?,error_text=? WHERE id=?`, v.ManifestUploaded, v.Status, timeString(v.UpdatedAt), v.ErrorText, v.ID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}
func scanImportPack(row interface{ Scan(...any) error }) (domain.ImportPack, error) {
	var v domain.ImportPack
	err := row.Scan(&v.SessionID, &v.PackID, &v.ExpectedSize, &v.ExpectedSHA256, &v.UploadedSize, &v.StagingPath, &v.Status)
	if err != nil {
		return v, mapNotFound(err)
	}
	return v, nil
}
func (s *ServerStore) GetImportPack(ctx context.Context, sid, pid string) (domain.ImportPack, error) {
	return scanImportPack(s.DB.QueryRowContext(ctx, `SELECT session_id,pack_id,expected_size,expected_sha256,uploaded_size,staging_path,status FROM import_packs WHERE session_id=? AND pack_id=?`, sid, pid))
}
func (s *ServerStore) UpdateImportPack(ctx context.Context, v domain.ImportPack) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE import_packs SET uploaded_size=?,status=? WHERE session_id=? AND pack_id=?`, v.UploadedSize, v.Status, v.SessionID, v.PackID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return domain.ErrNotFound
	}
	r, err = tx.ExecContext(ctx, `UPDATE packs SET uploaded_size=?,status=? WHERE id=?`, v.UploadedSize, v.Status, v.PackID)
	if err != nil {
		return err
	}
	n, _ = r.RowsAffected()
	if n == 0 {
		return domain.ErrNotFound
	}
	return tx.Commit()
}

// RegisterManifest attaches the uploaded manifest to a dedicated SQLite connection and atomically
// checks/registers the global PublishUnit declaration. The manifest itself remains immutable.
