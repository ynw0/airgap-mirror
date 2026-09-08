package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func (s *ServerStore) CreateSource(ctx context.Context, v domain.Source) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO sources(id,name,type,provider,upstream_url,root_path,public_url,enabled,config_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.Name, v.Type, v.Provider, v.UpstreamURL, v.RootPath, v.PublicURL, v.Enabled, []byte(v.ConfigJSON), timeString(v.CreatedAt), timeString(v.UpdatedAt)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO source_states(source_id,cursor_kind,cursor_value,updated_at) VALUES(?,?,?,?)`, v.ID, "", "", timeString(v.UpdatedAt)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *ServerStore) UpdateSource(ctx context.Context, v domain.Source) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldType domain.SourceType
	var oldProvider, oldRoot string
	var active sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT s.type,s.provider,s.root_path,st.active_epoch_id FROM sources s JOIN source_states st ON st.source_id=s.id WHERE s.id=?`, v.ID).Scan(&oldType, &oldProvider, &oldRoot, &active); err != nil {
		return mapNotFound(err)
	}
	if active.Valid && active.String != "" && (oldType != v.Type || oldProvider != v.Provider || oldRoot != v.RootPath) {
		return fmt.Errorf("source type/provider/rootPath cannot change during active epoch %s: %w", active.String, domain.ErrConflict)
	}
	r, err := tx.ExecContext(ctx, `UPDATE sources SET name=?,type=?,provider=?,upstream_url=?,root_path=?,public_url=?,enabled=?,config_json=?,updated_at=? WHERE id=?`, v.Name, v.Type, v.Provider, v.UpstreamURL, v.RootPath, v.PublicURL, v.Enabled, []byte(v.ConfigJSON), timeString(v.UpdatedAt), v.ID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return domain.ErrNotFound
	}
	return tx.Commit()
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
