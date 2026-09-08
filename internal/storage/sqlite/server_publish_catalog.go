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
