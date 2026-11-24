package sqlite

import (
	"context"
	"errors"
	"fmt"
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
	return s.exportCatalogSnapshot(ctx, sourceID, dest, "")
}

func (s *ServerStore) ExportRepairSnapshot(ctx context.Context, sourceID, dest, jobID string) (domain.Source, domain.SourceState, error) {
	if jobID == "" {
		return domain.Source{}, domain.SourceState{}, domain.ErrInvalid
	}
	return s.exportCatalogSnapshot(ctx, sourceID, dest, jobID)
}

func (s *ServerStore) exportCatalogSnapshot(ctx context.Context, sourceID, dest, repairJobID string) (domain.Source, domain.SourceState, error) {
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
	if repairJobID != "" {
		var valid int
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM repair_scans r JOIN maintenance_jobs j ON j.id=r.job_id JOIN sources src ON src.id=j.source_id JOIN source_states st ON st.source_id=j.source_id WHERE j.id=? AND j.source_id=? AND j.kind='REPAIR' AND j.status='COMPLETED' AND st.catalog_version=r.catalog_version AND src.updated_at=r.source_updated_at AND COALESCE(st.active_epoch_id,'')='' AND NOT EXISTS(SELECT 1 FROM maintenance_jobs active WHERE active.source_id=j.source_id AND active.status IN('QUEUED','RUNNING','PAUSED')))`, repairJobID, sourceID).Scan(&valid)
		if err != nil {
			return source, state, err
		}
		if valid != 1 || source.Type != domain.SourcePyPI || source.Provider != "pypi" {
			return source, state, fmt.Errorf("修复报告已失效或软件源正在同步，请重新检查: %w", domain.ErrConflict)
		}
	}
	_, err = tx.ExecContext(ctx, `CREATE TABLE snapshot.catalog_entries(source_id TEXT NOT NULL,logical_path TEXT NOT NULL,size INTEGER NOT NULL,sha256 TEXT NOT NULL,package_key TEXT NOT NULL DEFAULT '',version TEXT NOT NULL DEFAULT '',attributes BLOB,updated_at TEXT NOT NULL,PRIMARY KEY(source_id,logical_path)) WITHOUT ROWID; CREATE INDEX snapshot.ix_catalog_package ON catalog_entries(source_id,package_key);`)
	if err != nil {
		return source, state, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO snapshot.catalog_entries SELECT source_id,logical_path,size,sha256,package_key,version,attributes,updated_at FROM main.live_catalog_entries WHERE source_id=?`, sourceID)
	if err != nil {
		return source, state, err
	}
	if repairJobID != "" {
		// Only explicitly missing files are omitted from the exported baseline.
		// The live catalog is untouched, and incorrect-size files are never
		// silently overwritten by the normal immutable-artifact importer.
		if _, err = tx.ExecContext(ctx, `CREATE TABLE snapshot.repair_entries(source_id TEXT NOT NULL,logical_path TEXT NOT NULL,size INTEGER NOT NULL,sha256 TEXT NOT NULL,package_key TEXT NOT NULL,PRIMARY KEY(source_id,logical_path)) WITHOUT ROWID; CREATE INDEX snapshot.ix_repair_project ON repair_entries(source_id,package_key);`); err != nil {
			return source, state, err
		}
		result, e := tx.ExecContext(ctx, `INSERT INTO snapshot.repair_entries SELECT c.source_id,c.logical_path,c.size,c.sha256,c.package_key FROM snapshot.catalog_entries c JOIN main.repair_issues i ON i.job_id=? AND i.kind='MISSING' AND i.logical_path=c.logical_path AND i.size=c.size AND i.sha256=c.sha256 AND i.package_key=c.package_key`, repairJobID)
		if e != nil {
			return source, state, e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return source, state, e
		}
		var expected int64
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM main.repair_issues WHERE job_id=? AND kind='MISSING'`, repairJobID).Scan(&expected); err != nil {
			return source, state, err
		}
		if n == 0 || n != expected {
			return source, state, fmt.Errorf("没有可自动修复的缺失文件，或报告与账本不一致: %w", domain.ErrConflict)
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM snapshot.catalog_entries WHERE source_id=? AND logical_path IN(SELECT logical_path FROM snapshot.repair_entries WHERE source_id=?)`, sourceID, sourceID); err != nil {
			return source, state, err
		}
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
