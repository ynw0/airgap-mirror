package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"

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
	var oldProvider, oldRoot, oldUpstream, oldPublic, cursorKind, cursorValue string
	var oldConfig []byte
	var active sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT s.type,s.provider,s.root_path,s.upstream_url,s.public_url,s.config_json,st.cursor_kind,st.cursor_value,st.active_epoch_id FROM sources s JOIN source_states st ON st.source_id=s.id WHERE s.id=?`, v.ID).Scan(&oldType, &oldProvider, &oldRoot, &oldUpstream, &oldPublic, &oldConfig, &cursorKind, &cursorValue, &active); err != nil {
		return mapNotFound(err)
	}
	identityChanged := oldType != v.Type || oldProvider != v.Provider || oldRoot != v.RootPath || oldUpstream != v.UpstreamURL || !sameSyncConfig(oldConfig, v.ConfigJSON)
	if identityChanged || oldPublic != v.PublicURL || !reflect.DeepEqual(oldConfig, []byte(v.ConfigJSON)) {
		var maintenance int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM maintenance_jobs WHERE source_id=? AND status IN('QUEUED','RUNNING','PAUSED'))`, v.ID).Scan(&maintenance); err != nil {
			return err
		}
		if maintenance != 0 {
			return fmt.Errorf("软件源正在维护或暂停维护，不能修改仓库地址、类型和配置: %w", domain.ErrConflict)
		}
	}
	if active.Valid && active.String != "" && identityChanged {
		return fmt.Errorf("正在同步，不能修改软件源地址、类型或仓库目录: %w", domain.ErrConflict)
	}
	if identityChanged {
		var entries int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM live_catalog_entries WHERE source_id=? LIMIT 1)`, v.ID).Scan(&entries); err != nil {
			return err
		}
		if cursorKind != "" || cursorValue != "" || entries != 0 {
			return fmt.Errorf("该软件源已有同步记录，不能直接更换地址、类型或仓库目录；请新建软件源: %w", domain.ErrConflict)
		}
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

func sameSyncConfig(oldConfig, newConfig []byte) bool {
	// 不把清理策略或翻页大小当成新的软件源；其它配置变化仍要求使用新的源。
	var oldValue, newValue map[string]any
	if len(oldConfig) == 0 {
		oldConfig = []byte("{}")
	}
	if len(newConfig) == 0 {
		newConfig = []byte("{}")
	}
	if json.Unmarshal(oldConfig, &oldValue) != nil || json.Unmarshal(newConfig, &newValue) != nil {
		return false
	}
	for _, key := range []string{"gcOwnsPool", "changePageSize", "allDocsPageSize", "inventoryMode"} {
		delete(oldValue, key)
		delete(newValue, key)
	}
	return reflect.DeepEqual(oldValue, newValue)
}
