package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func (s *ServerStore) ExportSource(ctx context.Context, sourceID, dest string) error {
	_, _, err := s.ExportSourceSnapshot(ctx, sourceID, dest)
	return err
}

// ExportSourceSnapshot reads configuration, cursor and live generation from
// the very same SQLite read snapshot used to export the catalog.
func (s *ServerStore) ExportSourceSnapshot(ctx context.Context, sourceID, dest string) (domain.Source, domain.SourceState, error) {
	var source domain.Source
	var state domain.SourceState
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return source, state, err
	}
	if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return source, state, err
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return source, state, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `ATTACH DATABASE ? AS snapshot`, dest); err != nil {
		return source, state, err
	}
	defer conn.ExecContext(context.Background(), `DETACH DATABASE snapshot`)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return source, state, err
	}
	defer tx.Rollback()
	source, err = scanSource(tx.QueryRowContext(ctx, `SELECT id,name,type,provider,upstream_url,root_path,public_url,enabled,config_json,created_at,updated_at FROM sources WHERE id=?`, sourceID))
	if err != nil {
		return source, state, err
	}
	var updated string
	err = tx.QueryRowContext(ctx, `SELECT source_id,cursor_kind,cursor_value,catalog_version,live_bytes,live_objects,COALESCE(active_epoch_id,''),updated_at FROM source_states WHERE source_id=?`, sourceID).Scan(&state.SourceID, &state.LiveCursor.Kind, &state.LiveCursor.Value, &state.CatalogVersion, &state.LiveBytes, &state.LiveObjects, &state.ActiveEpochID, &updated)
	if err != nil {
		return source, state, mapNotFound(err)
	}
	state.UpdatedAt = parseTime(updated)
	_, err = tx.ExecContext(ctx, `CREATE TABLE snapshot.catalog_entries(source_id TEXT NOT NULL,logical_path TEXT NOT NULL,size INTEGER NOT NULL,sha256 TEXT NOT NULL,package_key TEXT NOT NULL DEFAULT '',version TEXT NOT NULL DEFAULT '',attributes BLOB,updated_at TEXT NOT NULL,PRIMARY KEY(source_id,logical_path)) WITHOUT ROWID; CREATE INDEX snapshot.ix_catalog_package ON catalog_entries(source_id,package_key);`)
	if err != nil {
		return source, state, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO snapshot.catalog_entries SELECT source_id,logical_path,size,sha256,package_key,version,attributes,updated_at FROM main.live_catalog_entries WHERE source_id=?`, sourceID)
	if err != nil {
		return source, state, err
	}
	return source, state, tx.Commit()
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
