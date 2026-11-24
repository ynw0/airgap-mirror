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

const catalogCopyBatch = 10000

func activeCatalogGeneration(ctx context.Context, tx *sql.Tx, sourceID string) (string, error) {
	var generation string
	err := tx.QueryRowContext(ctx, "SELECT generation FROM catalog_active_generation WHERE source_id=?", sourceID).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return generation, err
}

func upsertLiveCatalog(ctx context.Context, tx *sql.Tx, v domain.CatalogEntry) error {
	generation, err := activeCatalogGeneration(ctx, tx, v.SourceID)
	if err != nil {
		return err
	}
	if generation == "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO catalog_entries(source_id,logical_path,size,sha256,package_key,version,attributes,updated_at)
VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(source_id,logical_path) DO UPDATE SET size=excluded.size,sha256=excluded.sha256,package_key=excluded.package_key,version=excluded.version,attributes=excluded.attributes,updated_at=excluded.updated_at`,
			v.SourceID, v.LogicalPath, v.Size, v.SHA256, v.PackageKey, v.Version, []byte(v.Attributes), timeString(v.UpdatedAt))
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO catalog_generation_entries(source_id,generation,logical_path,size,sha256,package_key,version,attributes,updated_at)
VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(source_id,generation,logical_path) DO UPDATE SET size=excluded.size,sha256=excluded.sha256,package_key=excluded.package_key,version=excluded.version,attributes=excluded.attributes,updated_at=excluded.updated_at`,
			v.SourceID, generation, v.LogicalPath, v.Size, v.SHA256, v.PackageKey, v.Version, []byte(v.Attributes), timeString(v.UpdatedAt))
	}
	return err
}

// ReplaceCatalogFromFile is retained for callers without a maintenance job.
func (s *ServerStore) ReplaceCatalogFromFile(ctx context.Context, sourceID, rebuildPath string, expected domain.CatalogStats, at time.Time) error {
	id, err := domain.NewID()
	if err != nil {
		return err
	}
	return s.publishCatalogGeneration(ctx, id, "", sourceID, rebuildPath, expected, at)
}

// PublishInventoryCatalog uses the maintenance job ID as an idempotent generation
// identity, allowing a paused PyPI task to resume a partially copied generation.
// The old catalog is never removed or modified by the build.
func (s *ServerStore) PublishInventoryCatalog(ctx context.Context, jobID, sourceID, rebuildPath string, expected domain.CatalogStats, at time.Time) error {
	if jobID == "" {
		return domain.ErrInvalid
	}
	return s.publishCatalogGeneration(ctx, jobID, jobID, sourceID, rebuildPath, expected, at)
}

func (s *ServerStore) publishCatalogGeneration(ctx context.Context, generation, jobID, sourceID, rebuildPath string, expected domain.CatalogStats, at time.Time) error {
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
		return domain.ErrInvalid
	}

	state, err := s.GetState(ctx, sourceID)
	if err != nil {
		return err
	}
	if state.ActiveEpochID != "" {
		return fmt.Errorf("source has active epoch: %w", domain.ErrConflict)
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `ATTACH DATABASE ? AS rebuild`, abs); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `DETACH DATABASE rebuild`)

	var objects, bytes, invalid int64
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(size),0),COALESCE(SUM(CASE WHEN source_id<>? OR logical_path='' OR size<0 THEN 1 ELSE 0 END),0) FROM rebuild.catalog_entries`, sourceID).Scan(&objects, &bytes, &invalid); err != nil {
		return err
	}
	if invalid != 0 || objects != expected.Objects || bytes != expected.Bytes {
		return fmt.Errorf("rebuild catalog stats mismatch: %w", domain.ErrConflict)
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO catalog_generations(source_id,generation,expected_objects,expected_bytes,status)
VALUES(?,?,?,?,'BUILDING') ON CONFLICT(source_id,generation) DO NOTHING`, sourceID, generation, objects, bytes); err != nil {
		return err
	}

	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		done, err := copyCatalogGenerationBatch(ctx, conn, sourceID, generation, expected)
		if err != nil {
			return err
		}
		if done {
			break
		}
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var activeEpoch string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(active_epoch_id,'') FROM source_states WHERE source_id=?`, sourceID).Scan(&activeEpoch); err != nil {
		return mapNotFound(err)
	}
	if activeEpoch != "" {
		return fmt.Errorf("source has active epoch: %w", domain.ErrConflict)
	}
	if jobID != "" {
		var status domain.MaintenanceStatus
		if err = tx.QueryRowContext(ctx, `SELECT status FROM maintenance_jobs WHERE id=? AND source_id=? AND kind='INVENTORY'`, jobID, sourceID).Scan(&status); err != nil {
			return mapNotFound(err)
		}
		if status != domain.MaintenanceRunning {
			return fmt.Errorf("inventory is not running: %w", domain.ErrConflict)
		}
	}
	var copiedObjects, copiedBytes int64
	if err = tx.QueryRowContext(ctx, `SELECT copied_objects,copied_bytes FROM catalog_generations WHERE source_id=? AND generation=? AND status='BUILDING'`, sourceID, generation).Scan(&copiedObjects, &copiedBytes); err != nil {
		return err
	}
	if copiedObjects != objects || copiedBytes != bytes {
		return fmt.Errorf("incomplete catalog generation: %w", domain.ErrConflict)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_generations SET status='RETIRED' WHERE source_id=? AND status='LIVE'`, sourceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_generations SET status='LIVE' WHERE source_id=? AND generation=?`, sourceID, generation); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_active_generation(source_id,generation) VALUES(?,?) ON CONFLICT(source_id) DO UPDATE SET generation=excluded.generation`, sourceID, generation); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_states SET live_bytes=?,live_objects=?,catalog_version=catalog_version+1,updated_at=? WHERE source_id=?`, bytes, objects, timeString(at), sourceID); err != nil {
		return err
	}
	if jobID != "" {
		result, e := tx.ExecContext(ctx, `UPDATE maintenance_jobs SET status='COMPLETED',completed_at=?,error_text='' WHERE id=? AND status='RUNNING'`, timeString(at), jobID)
		if e != nil {
			return e
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return domain.ErrConflict
		}
	}
	return tx.Commit()
}

// Copy only a bounded key range per transaction. The cursor and rows are
// committed together. A crash can redo at most the unfinished batch.
func copyCatalogGenerationBatch(ctx context.Context, conn *sql.Conn, sourceID, generation string, expected domain.CatalogStats) (bool, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var last, status string
	var objects, bytes, copiedObjects, copiedBytes int64
	if err = tx.QueryRowContext(ctx, `SELECT last_path,status,expected_objects,expected_bytes,copied_objects,copied_bytes FROM catalog_generations WHERE source_id=? AND generation=?`, sourceID, generation).Scan(&last, &status, &objects, &bytes, &copiedObjects, &copiedBytes); err != nil {
		return false, err
	}
	if status != "BUILDING" || objects != expected.Objects || bytes != expected.Bytes {
		return false, domain.ErrConflict
	}
	if copiedObjects == objects && copiedBytes == bytes {
		return true, tx.Commit()
	}
	var end string
	var count, size int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(logical_path),''),COUNT(*),COALESCE(SUM(size),0) FROM (SELECT logical_path,size FROM rebuild.catalog_entries WHERE source_id=? AND logical_path>? ORDER BY logical_path LIMIT ?)`, sourceID, last, catalogCopyBatch).Scan(&end, &count, &size); err != nil {
		return false, err
	}
	if count <= 0 || end <= last || copiedObjects+count > objects || copiedBytes+size > bytes {
		return false, fmt.Errorf("inconsistent catalog copy checkpoint: %w", domain.ErrConflict)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO catalog_generation_entries(source_id,generation,logical_path,size,sha256,package_key,version,attributes,updated_at)
SELECT source_id,?,logical_path,size,sha256,package_key,version,attributes,updated_at FROM rebuild.catalog_entries WHERE source_id=? AND logical_path>? AND logical_path<=?`, generation, sourceID, last, end)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != count {
		return false, domain.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_generations SET last_path=?,copied_objects=copied_objects+?,copied_bytes=copied_bytes+? WHERE source_id=? AND generation=?`, end, count, size, sourceID, generation); err != nil {
		return false, err
	}
	return false, tx.Commit()
}
