package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

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
		_, err = tx.ExecContext(ctx, `INSERT INTO publish_metadata_entries(entry_id,publish_unit_id,source_id,logical_path,size,sha256,operation,staged_path,package_key,version,attributes) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(entry_id) DO UPDATE SET operation=excluded.operation,staged_path=excluded.staged_path,package_key=excluded.package_key,version=excluded.version,attributes=excluded.attributes`, m.EntryID, a.PublishUnitID, a.SourceID, a.LogicalPath, a.Size, a.SHA256, a.Operation, stagedPath, a.PackageKey, a.Version, []byte(a.Attributes))
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
	rows, err := s.DB.QueryContext(ctx, `SELECT entry_id,publish_unit_id,source_id,logical_path,size,sha256,operation,staged_path,package_key,version,attributes FROM publish_metadata_entries WHERE publish_unit_id=? ORDER BY logical_path`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.PublishMetadataEntry
	for rows.Next() {
		var v domain.PublishMetadataEntry
		var attrs []byte
		if err = rows.Scan(&v.EntryID, &v.PublishUnitID, &v.SourceID, &v.LogicalPath, &v.Size, &v.SHA256, &v.Operation, &v.StagedPath, &v.PackageKey, &v.Version, &attrs); err != nil {
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
