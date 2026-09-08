package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func (s *ServerStore) ReplaceCatalogFromFile(ctx context.Context, sourceID, rebuildPath string, expected domain.CatalogStats, at time.Time) error {
	if sourceID == "" || rebuildPath == "" || expected.SourceID != sourceID || expected.Bytes < 0 || expected.Objects < 0 {
		return fmt.Errorf("invalid catalog replacement request: %w", domain.ErrInvalid)
	}
	abs, err := filepath.Abs(rebuildPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("rebuild catalog is not a regular file: %w", domain.ErrInvalid)
	}

	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `ATTACH DATABASE ? AS rebuild`, abs); err != nil {
		return fmt.Errorf("attach rebuild catalog: %w", err)
	}
	defer conn.ExecContext(context.Background(), `DETACH DATABASE rebuild`)

	var distinctSources int64
	var minSource, maxSource string
	var bytes, objects int64
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(DISTINCT source_id),COALESCE(MIN(source_id),''),COALESCE(MAX(source_id),''),COALESCE(SUM(size),0),COUNT(*) FROM rebuild.catalog_entries`).Scan(&distinctSources, &minSource, &maxSource, &bytes, &objects); err != nil {
		return fmt.Errorf("inspect rebuild catalog: %w", err)
	}
	if objects > 0 && (distinctSources != 1 || minSource != sourceID || maxSource != sourceID) {
		return fmt.Errorf("rebuild catalog contains entries for another source: %w", domain.ErrConflict)
	}
	if bytes != expected.Bytes || objects != expected.Objects {
		return fmt.Errorf("rebuild catalog stats changed expected=%d/%d actual=%d/%d: %w", expected.Objects, expected.Bytes, objects, bytes, domain.ErrConflict)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT active_epoch_id FROM source_states WHERE source_id=?`, sourceID).Scan(&active); err != nil {
		return mapNotFound(err)
	}
	if active.Valid && active.String != "" {
		return fmt.Errorf("source %s has active epoch %s; inventory cannot replace live catalog: %w", sourceID, active.String, domain.ErrConflict)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM catalog_entries WHERE source_id=?`, sourceID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO catalog_entries(source_id,logical_path,size,sha256,package_key,version,attributes,updated_at) SELECT source_id,logical_path,size,sha256,package_key,version,attributes,updated_at FROM rebuild.catalog_entries WHERE source_id=?`, sourceID)
	if err != nil {
		return fmt.Errorf("install rebuild catalog: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted != expected.Objects {
		return fmt.Errorf("catalog replacement inserted %d entries, expected %d: %w", inserted, expected.Objects, domain.ErrConflict)
	}
	result, err = tx.ExecContext(ctx, `UPDATE source_states SET live_bytes=?,live_objects=?,catalog_version=catalog_version+1,updated_at=? WHERE source_id=? AND COALESCE(active_epoch_id,'')=''`, expected.Bytes, expected.Objects, timeString(at), sourceID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return fmt.Errorf("source state changed during catalog replacement: %w", domain.ErrConflict)
	}
	return tx.Commit()
}
