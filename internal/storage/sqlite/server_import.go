package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func sameEpoch(e domain.Epoch, d domain.BatchDescriptor) bool {
	return e.ID == d.EpochID && e.SourceID == d.SourceID && e.BaseCursor.Equal(d.BaseCursor) && e.TargetCursor.Equal(d.TargetCursor) && e.TotalBytes == d.EpochTotalBytes && e.TotalObjects == d.EpochTotalObjects && e.TotalBatches == d.EpochTotalBatches && e.PublishUnitCount == d.EpochPublishUnitCount
}
func sameBatch(b domain.Batch, d domain.BatchDescriptor) bool {
	return b.ID == d.BatchID && b.EpochID == d.EpochID && b.SourceID == d.SourceID && b.Sequence == d.BatchSequence && b.PlannedBytes == d.BatchBytes && b.ObjectCount == d.BatchObjects && b.PackCount == len(d.Packs)
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

	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM import_sessions WHERE request_id=?`, session.RequestID).Scan(&existingID)
	if err == nil {
		var out domain.ImportSession
		var created, updated string
		err = tx.QueryRowContext(ctx, `SELECT id,request_id,source_id,epoch_id,batch_id,manifest_path,manifest_expected_size,manifest_expected_sha256,manifest_uploaded_size,status,staging_path,created_at,updated_at,error_text FROM import_sessions WHERE id=?`, existingID).Scan(&out.ID, &out.RequestID, &out.SourceID, &out.EpochID, &out.BatchID, &out.ManifestPath, &out.ManifestSize, &out.ManifestSHA256, &out.ManifestUploaded, &out.Status, &out.StagingPath, &created, &updated, &out.ErrorText)
		if err != nil {
			return domain.ImportSession{}, err
		}
		if out.SourceID != d.SourceID || out.EpochID != d.EpochID || out.BatchID != d.BatchID || out.ManifestSize != d.ManifestSize || out.ManifestSHA256 != d.ManifestSHA256 {
			return domain.ImportSession{}, fmt.Errorf("request id reused for another bundle: %w", domain.ErrConflict)
		}
		out.CreatedAt = parseTime(created)
		out.UpdatedAt = parseTime(updated)
		if err = tx.Commit(); err != nil {
			return domain.ImportSession{}, err
		}
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.ImportSession{}, err
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
	r, err := s.DB.ExecContext(ctx, `UPDATE import_packs SET uploaded_size=?,status=? WHERE session_id=? AND pack_id=?`, v.UploadedSize, v.Status, v.SessionID, v.PackID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return domain.ErrNotFound
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE packs SET uploaded_size=?,status=? WHERE id=?`, v.UploadedSize, v.Status, v.PackID)
	return err
}

// RegisterManifest attaches the uploaded manifest to a dedicated SQLite connection and atomically
// checks/registers the global PublishUnit declaration. The manifest itself remains immutable.
func (s *ServerStore) RegisterManifest(ctx context.Context, sessionID string) error {
	sess, err := s.GetImportSession(ctx, sessionID)
	if err != nil {
		return err
	}
	ep, err := s.GetEpoch(ctx, sess.EpochID)
	if err != nil {
		return err
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `ATTACH DATABASE ? AS incoming`, sess.ManifestPath); err != nil {
		return fmt.Errorf("attach manifest: %w", err)
	}
	defer conn.ExecContext(context.Background(), `DETACH DATABASE incoming`)
	meta := func(key string) (string, error) {
		var v string
		e := conn.QueryRowContext(ctx, `SELECT value FROM incoming.manifest_meta WHERE key=?`, key).Scan(&v)
		return v, e
	}
	sourceID, err := meta("source_id")
	if err != nil {
		return err
	}
	epochID, err := meta("epoch_id")
	if err != nil {
		return err
	}
	batchID, err := meta("batch_id")
	if err != nil {
		return err
	}
	if sourceID != sess.SourceID || epochID != sess.EpochID || batchID != sess.BatchID {
		return fmt.Errorf("manifest identity mismatch: %w", domain.ErrConflict)
	}
	schemaVersion, err := meta("schema_version")
	if err != nil {
		return err
	}
	if schemaVersion != fmt.Sprint(domain.ManifestSchemaVersion) {
		return fmt.Errorf("unsupported manifest schema %s: %w", schemaVersion, domain.ErrInvalid)
	}
	batch, err := s.GetBatch(ctx, sess.BatchID)
	if err != nil {
		return err
	}
	var invalid int64
	checks := []struct {
		query string
		args  []any
		name  string
	}{
		{`SELECT COUNT(*) FROM incoming.publish_units WHERE epoch_id<>? OR source_id<>? OR required_count<0`, []any{sess.EpochID, sess.SourceID}, "publish unit identity"},
		{`SELECT COUNT(*) FROM incoming.entries WHERE epoch_id<>? OR source_id<>? OR size<0 OR length(sha256)<>64 OR operation NOT IN('ADD','UPDATE') OR pack_offset<? OR record_length<=0`, []any{sess.EpochID, sess.SourceID, 128}, "entry identity"},
		{`SELECT COUNT(*) FROM incoming.entries e LEFT JOIN incoming.publish_units u ON u.id=e.publish_unit_id WHERE u.id IS NULL`, nil, "entry publish unit"},
		{`SELECT COUNT(*) FROM incoming.entries e LEFT JOIN main.packs p ON p.id=e.pack_id AND p.batch_id=? WHERE p.id IS NULL`, []any{sess.BatchID}, "entry pack"},
		{`SELECT COUNT(*) FROM (SELECT logical_path FROM incoming.entries GROUP BY logical_path HAVING COUNT(*)>1)`, nil, "duplicate logical path"},
		{`SELECT COUNT(*) FROM incoming.publish_units u WHERE u.required_count < (SELECT COUNT(*) FROM incoming.entries e WHERE e.publish_unit_id=u.id)`, nil, "publish unit required count"},
	}
	for _, check := range checks {
		if err = conn.QueryRowContext(ctx, check.query, check.args...).Scan(&invalid); err != nil {
			return err
		}
		if invalid != 0 {
			return fmt.Errorf("manifest %s validation failed (%d rows): %w", check.name, invalid, domain.ErrConflict)
		}
	}
	var entryCount int64
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM incoming.entries`).Scan(&entryCount); err != nil {
		return err
	}
	if entryCount != batch.ObjectCount {
		return fmt.Errorf("manifest entries %d != batch objects %d: %w", entryCount, batch.ObjectCount, domain.ErrConflict)
	}
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM main.packs p WHERE p.batch_id=? AND p.entry_count<>(SELECT COUNT(*) FROM incoming.entries e WHERE e.pack_id=p.id)`, sess.BatchID).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return fmt.Errorf("manifest pack entry counts mismatch (%d packs): %w", invalid, domain.ErrConflict)
	}
	var count int64
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM incoming.publish_units`).Scan(&count); err != nil {
		return err
	}
	if count != ep.PublishUnitCount {
		return fmt.Errorf("manifest publish unit count %d != epoch %d: %w", count, ep.PublishUnitCount, domain.ErrConflict)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var conflicts int64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM incoming.publish_units i JOIN main.publish_units p ON p.id=i.id WHERE p.epoch_id<>i.epoch_id OR p.source_id<>i.source_id OR p.unit_key<>i.unit_key OR p.required_count<>i.required_count OR p.metadata_path<>i.metadata_path`).Scan(&conflicts)
	if err != nil {
		return err
	}
	if conflicts != 0 {
		return fmt.Errorf("publish unit declaration conflict: %w", domain.ErrConflict)
	}
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM incoming.publish_units i JOIN main.publish_units p ON p.epoch_id=i.epoch_id AND p.unit_key=i.unit_key WHERE p.id<>i.id`).Scan(&conflicts)
	if err != nil {
		return err
	}
	if conflicts != 0 {
		return fmt.Errorf("publish unit key conflict: %w", domain.ErrConflict)
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO main.publish_units(id,epoch_id,source_id,unit_key,status,required_count,imported_count,metadata_path) SELECT id,epoch_id,source_id,unit_key,?,required_count,0,metadata_path FROM incoming.publish_units`, domain.PublishWaiting)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE import_sessions SET status='MANIFEST_READY',updated_at=? WHERE id=?`, timeString(time.Now()), sessionID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ServerStore) CommitImportedEntry(ctx context.Context, m domain.ManifestEntry, c domain.CatalogEntry, stagedPath string) (bool, error) {
	a := m.Artifact
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var sid, path, sha, unit string
	var metadata bool
	err = tx.QueryRowContext(ctx, `SELECT source_id,logical_path,sha256,publish_unit_id,metadata FROM imported_entries WHERE epoch_id=? AND entry_id=?`, a.EpochID, m.EntryID).Scan(&sid, &path, &sha, &unit, &metadata)
	if err == nil {
		if sid != a.SourceID || path != a.LogicalPath || sha != a.SHA256 || unit != a.PublishUnitID || metadata != a.Metadata {
			return false, fmt.Errorf("imported entry identity mismatch: %w", domain.ErrConflict)
		}
		return false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO imported_entries(epoch_id,entry_id,source_id,logical_path,sha256,publish_unit_id,metadata,imported_at) VALUES(?,?,?,?,?,?,?,?)`, a.EpochID, m.EntryID, a.SourceID, a.LogicalPath, a.SHA256, a.PublishUnitID, a.Metadata, timeString(time.Now()))
	if err != nil {
		return false, err
	}
	if a.Metadata {
		_, err = tx.ExecContext(ctx, `INSERT INTO publish_metadata_entries(entry_id,publish_unit_id,source_id,logical_path,size,sha256,staged_path,package_key,version,attributes) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(entry_id) DO UPDATE SET staged_path=excluded.staged_path,package_key=excluded.package_key,version=excluded.version,attributes=excluded.attributes`, m.EntryID, a.PublishUnitID, a.SourceID, a.LogicalPath, a.Size, a.SHA256, stagedPath, a.PackageKey, a.Version, []byte(a.Attributes))
		if err != nil {
			return false, err
		}
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO catalog_entries(source_id,logical_path,size,sha256,package_key,version,attributes,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(source_id,logical_path) DO UPDATE SET size=excluded.size,sha256=excluded.sha256,package_key=excluded.package_key,version=excluded.version,attributes=excluded.attributes,updated_at=excluded.updated_at`, c.SourceID, c.LogicalPath, c.Size, c.SHA256, c.PackageKey, c.Version, []byte(c.Attributes), timeString(c.UpdatedAt))
		if err != nil {
			return false, err
		}
	}
	r, err := tx.ExecContext(ctx, `UPDATE publish_units SET imported_count=imported_count+1,status=CASE WHEN imported_count+1=required_count THEN ? ELSE status END WHERE id=? AND imported_count<required_count`, domain.PublishReady, a.PublishUnitID)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return false, fmt.Errorf("publish unit count overflow or missing for %s: %w", a.PublishUnitID, domain.ErrConflict)
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *ServerStore) ListPublishMetadata(ctx context.Context, unitID string) ([]domain.PublishMetadataEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT entry_id,publish_unit_id,source_id,logical_path,size,sha256,staged_path,package_key,version,attributes FROM publish_metadata_entries WHERE publish_unit_id=? ORDER BY logical_path`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.PublishMetadataEntry
	for rows.Next() {
		var v domain.PublishMetadataEntry
		var attrs []byte
		if err = rows.Scan(&v.EntryID, &v.PublishUnitID, &v.SourceID, &v.LogicalPath, &v.Size, &v.SHA256, &v.StagedPath, &v.PackageKey, &v.Version, &attrs); err != nil {
			return nil, err
		}
		v.Attributes = attrs
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *ServerStore) MarkImportPackCommitted(ctx context.Context, sessionID, packID string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE import_packs SET status=? WHERE session_id=? AND pack_id=?`, domain.PackImported, sessionID, packID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return domain.ErrNotFound
	}
	_, err = tx.ExecContext(ctx, `UPDATE packs SET status=?,uploaded_size=size WHERE id=?`, domain.PackImported, packID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
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
func (s *ServerStore) ExportSource(ctx context.Context, sourceID, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `ATTACH DATABASE ? AS snapshot`, dest); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `DETACH DATABASE snapshot`)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `CREATE TABLE snapshot.catalog_entries(source_id TEXT NOT NULL,logical_path TEXT NOT NULL,size INTEGER NOT NULL,sha256 TEXT NOT NULL,package_key TEXT NOT NULL DEFAULT '',version TEXT NOT NULL DEFAULT '',attributes BLOB,updated_at TEXT NOT NULL,PRIMARY KEY(source_id,logical_path)) WITHOUT ROWID; CREATE INDEX snapshot.ix_catalog_package ON catalog_entries(source_id,package_key);`)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO snapshot.catalog_entries SELECT source_id,logical_path,size,sha256,package_key,version,attributes,updated_at FROM main.catalog_entries WHERE source_id=?`, sourceID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ServerStore) ListImportPacks(ctx context.Context, sessionID string) ([]domain.ImportPack, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT session_id,pack_id,expected_size,expected_sha256,uploaded_size,staging_path,status FROM import_packs WHERE session_id=? ORDER BY pack_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ImportPack
	for rows.Next() {
		v, e := scanImportPack(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
