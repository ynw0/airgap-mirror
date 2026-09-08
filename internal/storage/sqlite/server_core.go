package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type ServerStore struct{ DB *sql.DB }

func NewServerStore(db *sql.DB) *ServerStore { return &ServerStore{DB: db} }

func timeString(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func parseTime(s string) time.Time  { t, _ := time.Parse(time.RFC3339Nano, s); return t }
func parseOptionalTime(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTime(s.String)
	return &t
}
func mapNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

func (s *ServerStore) CreateSource(ctx context.Context, v domain.Source) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sources(id,name,type,provider,upstream_url,root_path,public_url,enabled,config_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.Name, v.Type, v.Provider, v.UpstreamURL, v.RootPath, v.PublicURL, v.Enabled, []byte(v.ConfigJSON), timeString(v.CreatedAt), timeString(v.UpdatedAt))
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO source_states(source_id,cursor_kind,cursor_value,updated_at) VALUES(?,?,?,?)`, v.ID, "", "", timeString(v.UpdatedAt))
	return err
}
func (s *ServerStore) UpdateSource(ctx context.Context, v domain.Source) error {
	r, err := s.DB.ExecContext(ctx, `UPDATE sources SET name=?,type=?,provider=?,upstream_url=?,root_path=?,public_url=?,enabled=?,config_json=?,updated_at=? WHERE id=?`, v.Name, v.Type, v.Provider, v.UpstreamURL, v.RootPath, v.PublicURL, v.Enabled, []byte(v.ConfigJSON), timeString(v.UpdatedAt), v.ID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}
func scanSource(row interface{ Scan(...any) error }) (domain.Source, error) {
	var v domain.Source
	var typ string
	var cfg []byte
	var created, updated string
	err := row.Scan(&v.ID, &v.Name, &typ, &v.Provider, &v.UpstreamURL, &v.RootPath, &v.PublicURL, &v.Enabled, &cfg, &created, &updated)
	if err != nil {
		return v, mapNotFound(err)
	}
	v.Type = domain.SourceType(typ)
	v.ConfigJSON = cfg
	v.CreatedAt = parseTime(created)
	v.UpdatedAt = parseTime(updated)
	return v, nil
}
func (s *ServerStore) GetSource(ctx context.Context, id string) (domain.Source, error) {
	return scanSource(s.DB.QueryRowContext(ctx, `SELECT id,name,type,provider,upstream_url,root_path,public_url,enabled,config_json,created_at,updated_at FROM sources WHERE id=?`, id))
}
func (s *ServerStore) ListSources(ctx context.Context) ([]domain.Source, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,type,provider,upstream_url,root_path,public_url,enabled,config_json,created_at,updated_at FROM sources ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Source
	for rows.Next() {
		v, e := scanSource(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *ServerStore) GetState(ctx context.Context, id string) (domain.SourceState, error) {
	var v domain.SourceState
	var kind, value, updated string
	var active sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT source_id,cursor_kind,cursor_value,catalog_version,live_bytes,live_objects,active_epoch_id,updated_at FROM source_states WHERE source_id=?`, id).Scan(&v.SourceID, &kind, &value, &v.CatalogVersion, &v.LiveBytes, &v.LiveObjects, &active, &updated)
	if err != nil {
		return v, mapNotFound(err)
	}
	v.LiveCursor = domain.Cursor{Kind: kind, Value: value}
	if active.Valid {
		v.ActiveEpochID = active.String
	}
	v.UpdatedAt = parseTime(updated)
	return v, nil
}
func (s *ServerStore) PutState(ctx context.Context, v domain.SourceState) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO source_states(source_id,cursor_kind,cursor_value,catalog_version,live_bytes,live_objects,active_epoch_id,updated_at) VALUES(?,?,?,?,?,?,NULLIF(?,''),?) ON CONFLICT(source_id) DO UPDATE SET cursor_kind=excluded.cursor_kind,cursor_value=excluded.cursor_value,catalog_version=excluded.catalog_version,live_bytes=excluded.live_bytes,live_objects=excluded.live_objects,active_epoch_id=excluded.active_epoch_id,updated_at=excluded.updated_at`, v.SourceID, v.LiveCursor.Kind, v.LiveCursor.Value, v.CatalogVersion, v.LiveBytes, v.LiveObjects, v.ActiveEpochID, timeString(v.UpdatedAt))
	return err
}

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

func (s *ServerStore) DeclarePublishUnit(ctx context.Context, u domain.PublishUnit) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var eid, sid, key string
	var required int64
	err = tx.QueryRowContext(ctx, `SELECT epoch_id,source_id,unit_key,required_count FROM publish_units WHERE id=?`, u.ID).Scan(&eid, &sid, &key, &required)
	if err == nil {
		if eid != u.EpochID || sid != u.SourceID || key != u.Key || required != u.Required {
			return fmt.Errorf("publish unit declaration mismatch: %w", domain.ErrConflict)
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO publish_units(id,epoch_id,source_id,unit_key,status,required_count,imported_count,metadata_path) VALUES(?,?,?,?,?,?,0,?)`, u.ID, u.EpochID, u.SourceID, u.Key, domain.PublishWaiting, u.Required, u.MetadataPath)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func scanPublishUnit(row interface{ Scan(...any) error }) (domain.PublishUnit, error) {
	var u domain.PublishUnit
	var published sql.NullString
	err := row.Scan(&u.ID, &u.EpochID, &u.SourceID, &u.Key, &u.Status, &u.Required, &u.Imported, &u.MetadataPath, &published)
	if err != nil {
		return u, mapNotFound(err)
	}
	u.PublishedAt = parseOptionalTime(published)
	return u, nil
}
func (s *ServerStore) GetPublishUnit(ctx context.Context, id string) (domain.PublishUnit, error) {
	return scanPublishUnit(s.DB.QueryRowContext(ctx, `SELECT id,epoch_id,source_id,unit_key,status,required_count,imported_count,metadata_path,published_at FROM publish_units WHERE id=?`, id))
}
func (s *ServerStore) ListPublishUnits(ctx context.Context, epochID string) ([]domain.PublishUnit, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,epoch_id,source_id,unit_key,status,required_count,imported_count,metadata_path,published_at FROM publish_units WHERE epoch_id=? ORDER BY unit_key`, epochID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.PublishUnit
	for rows.Next() {
		v, e := scanPublishUnit(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *ServerStore) MarkPublished(ctx context.Context, id string, at time.Time) error {
	r, err := s.DB.ExecContext(ctx, `UPDATE publish_units SET status=?,published_at=? WHERE id=? AND imported_count=required_count`, domain.PublishPublished, timeString(at), id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return domain.ErrConflict
	}
	return nil
}

// CatalogStore implementation.
func (s *ServerStore) Get(ctx context.Context, sourceID, path string) (domain.CatalogEntry, error) {
	var v domain.CatalogEntry
	var attrs []byte
	var updated string
	err := s.DB.QueryRowContext(ctx, `SELECT source_id,logical_path,size,sha256,package_key,version,attributes,updated_at FROM catalog_entries WHERE source_id=? AND logical_path=?`, sourceID, path).Scan(&v.SourceID, &v.LogicalPath, &v.Size, &v.SHA256, &v.PackageKey, &v.Version, &attrs, &updated)
	if err != nil {
		return v, mapNotFound(err)
	}
	v.Attributes = attrs
	v.UpdatedAt = parseTime(updated)
	return v, nil
}
func (s *ServerStore) ListByPackage(ctx context.Context, sourceID, key string) ([]domain.CatalogEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT source_id,logical_path,size,sha256,package_key,version,attributes,updated_at FROM catalog_entries WHERE source_id=? AND package_key=? ORDER BY logical_path`, sourceID, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CatalogEntry
	for rows.Next() {
		var v domain.CatalogEntry
		var attrs []byte
		var updated string
		if err = rows.Scan(&v.SourceID, &v.LogicalPath, &v.Size, &v.SHA256, &v.PackageKey, &v.Version, &attrs, &updated); err != nil {
			return nil, err
		}
		v.Attributes = attrs
		v.UpdatedAt = parseTime(updated)
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *ServerStore) Upsert(ctx context.Context, v domain.CatalogEntry) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO catalog_entries(source_id,logical_path,size,sha256,package_key,version,attributes,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(source_id,logical_path) DO UPDATE SET size=excluded.size,sha256=excluded.sha256,package_key=excluded.package_key,version=excluded.version,attributes=excluded.attributes,updated_at=excluded.updated_at`, v.SourceID, v.LogicalPath, v.Size, v.SHA256, v.PackageKey, v.Version, []byte(v.Attributes), timeString(v.UpdatedAt))
	return err
}
func (s *ServerStore) Delete(ctx context.Context, sourceID, path string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM catalog_entries WHERE source_id=? AND logical_path=?`, sourceID, path)
	return err
}
func (s *ServerStore) Stats(ctx context.Context, sourceID string) (domain.CatalogStats, error) {
	v := domain.CatalogStats{SourceID: sourceID}
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(size),0),COUNT(*) FROM catalog_entries WHERE source_id=?`, sourceID).Scan(&v.Bytes, &v.Objects)
	return v, err
}

func (s *ServerStore) WriteAudit(ctx context.Context, e domain.AuditEvent) error {
	detail := []byte(e.Detail)
	if detail == nil {
		detail, _ = json.Marshal(map[string]any{})
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO audit_logs(id,kind,source_id,epoch_id,batch_id,message,detail,created_at) VALUES(?,?,?,?,?,?,?,?)`, e.ID, e.Kind, e.SourceID, e.EpochID, e.BatchID, e.Message, detail, timeString(e.CreatedAt))
	return err
}
