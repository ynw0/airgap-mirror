package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

// projectInventory is a private, durable catalog built by PyPI inventory.
// Its cursor, statistics, and catalog rows commit in the SAME transaction.
// The production catalog is not touched until the entire scan passes validation.
type projectInventory struct {
	path      string
	sourceID  string
	db        *sql.DB
	tx        *sql.Tx
	progress  ports.InventoryProgress
	unflushed int
	unflushedObjects int64
	lastFlush time.Time
	closed    bool
}

func (f *RebuildCatalogFactory) projectPath(jobID string) (string, error) {
	if f.Root == "" || jobID == "" || strings.ContainsAny(jobID, "/\\") {
		return "", fmt.Errorf("invalid resumable inventory path: %w", domain.ErrInvalid)
	}
	root, err := filepath.Abs(f.Root)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(root, 0750); err != nil {
		return "", err
	}
	return filepath.Join(root, "catalog-"+jobID+".sqlite"), nil
}

func (f *RebuildCatalogFactory) OpenProjectInventory(ctx context.Context, jobID, sourceID string) (ports.ProjectInventorySink, error) {
	path, err := f.projectPath(jobID)
	if err != nil || sourceID == "" {
		return nil, fmt.Errorf("open PyPI checkpoint: %w", domain.ErrInvalid)
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("checkpoint is not a regular file: %w", domain.ErrConflict)
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
		if createErr != nil {
			return nil, createErr
		}
		if createErr = file.Close(); createErr != nil {
			return nil, createErr
		}
	} else {
		return nil, statErr
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	const ddl = `PRAGMA journal_mode=WAL;
PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS catalog_entries(
	source_id TEXT NOT NULL, logical_path TEXT NOT NULL, size INTEGER NOT NULL,
	sha256 TEXT NOT NULL, package_key TEXT NOT NULL DEFAULT '',
	version TEXT NOT NULL DEFAULT '', attributes BLOB, updated_at TEXT NOT NULL,
	PRIMARY KEY(source_id,logical_path)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_catalog_package ON catalog_entries(source_id,package_key);
CREATE TABLE IF NOT EXISTS inventory_checkpoint(
	id INTEGER PRIMARY KEY CHECK(id=1),
	source_id TEXT NOT NULL, signature TEXT NOT NULL DEFAULT '',
	project_hash TEXT NOT NULL DEFAULT '', total_projects INTEGER NOT NULL DEFAULT 0,
	completed_projects INTEGER NOT NULL DEFAULT 0, last_project TEXT NOT NULL DEFAULT '',
	scanned_objects INTEGER NOT NULL DEFAULT 0, scanned_bytes INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS inventory_pages(
	project TEXT PRIMARY KEY, size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL
) WITHOUT ROWID;`
	if _, err = db.ExecContext(ctx, ddl); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO inventory_checkpoint(id,source_id) VALUES(1,?) ON CONFLICT(id) DO NOTHING`, sourceID); err != nil {
		db.Close()
		return nil, err
	}
	var saved string
	if err = db.QueryRowContext(ctx, `SELECT source_id FROM inventory_checkpoint WHERE id=1`).Scan(&saved); err != nil {
		db.Close()
		return nil, err
	}
	if saved != sourceID {
		db.Close()
		return nil, fmt.Errorf("checkpoint belongs to another source: %w", domain.ErrConflict)
	}
	return &projectInventory{path: path, sourceID: sourceID, db: db}, nil
}

func (c *projectInventory) Restore(ctx context.Context, signature, projectHash string, total int64) (ports.InventoryProgress, error) {
	if c.closed || c.tx != nil || signature == "" || projectHash == "" || total < 0 {
		return c.progress, fmt.Errorf("invalid checkpoint restore: %w", domain.ErrInvalid)
	}
	var oldSig, oldHash, last string
	var savedTotal, done, objects, bytes int64
	err := c.db.QueryRowContext(ctx, `SELECT signature,project_hash,total_projects,completed_projects,last_project,scanned_objects,scanned_bytes
FROM inventory_checkpoint WHERE id=1`).Scan(&oldSig, &oldHash, &savedTotal, &done, &last, &objects, &bytes)
	if err != nil {
		return c.progress, err
	}
	if oldSig == "" {
		_, err = c.db.ExecContext(ctx, `UPDATE inventory_checkpoint SET signature=?,project_hash=?,total_projects=? WHERE id=1 AND signature=''`, signature, projectHash, total)
		if err != nil {
			return c.progress, err
		}
	} else if oldSig != signature || oldHash != projectHash || savedTotal != total {
		return c.progress, fmt.Errorf("软件源或项目目录在清点后发生变化；请取消旧清点并重新开始: %w", domain.ErrConflict)
	}
	if done < 0 || done > total || objects < 0 || bytes < 0 {
		return c.progress, fmt.Errorf("checkpoint counters are invalid: %w", domain.ErrConflict)
	}
	c.progress = ports.InventoryProgress{ScannedObjects: objects, ScannedBytes: bytes, CompletedProjects: done, TotalProjects: total, LastProject: last}
	c.lastFlush = time.Now()
	c.tx, err = c.db.BeginTx(ctx, nil)
	return c.progress, err
}

func (c *projectInventory) Put(ctx context.Context, entry domain.CatalogEntry) error {
	if c.closed || c.tx == nil || entry.SourceID != c.sourceID || entry.LogicalPath == "" || entry.Size < 0 || len(entry.SHA256) != 64 {
		return fmt.Errorf("invalid PyPI catalog entry: %w", domain.ErrInvalid)
	}
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now()
	}
	result, err := c.tx.ExecContext(ctx, `INSERT INTO catalog_entries(source_id,logical_path,size,sha256,package_key,version,attributes,updated_at)
VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(source_id,logical_path) DO NOTHING`,
		entry.SourceID, entry.LogicalPath, entry.Size, strings.ToLower(entry.SHA256),
		entry.PackageKey, entry.Version, []byte(entry.Attributes), timeString(entry.UpdatedAt))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var oldSize int64
		var oldSum string
		if err = c.tx.QueryRowContext(ctx, `SELECT size,sha256 FROM catalog_entries WHERE source_id=? AND logical_path=?`, entry.SourceID, entry.LogicalPath).Scan(&oldSize, &oldSum); err != nil {
			return err
		}
		if oldSize != entry.Size || !strings.EqualFold(oldSum, entry.SHA256) {
			return fmt.Errorf("PyPI file %s has conflicting index references: %w", entry.LogicalPath, domain.ErrConflict)
		}
		return nil
	}
	c.progress.ScannedObjects++
	c.unflushedObjects++
	c.progress.ScannedBytes += entry.Size
	return nil
}

func (c *projectInventory) CheckpointProject(ctx context.Context, project string, size, mtimeNS int64) (ports.InventoryProgress, bool, error) {
	if c.closed || c.tx == nil || project == "" || size < 0 || project <= c.progress.LastProject {
		return c.progress, false, fmt.Errorf("invalid or out-of-order project checkpoint: %w", domain.ErrConflict)
	}
	if _, err := c.tx.ExecContext(ctx, `INSERT INTO inventory_pages(project,size,mtime_ns) VALUES(?,?,?)`, project, size, mtimeNS); err != nil {
		return c.progress, false, err
	}
	c.progress.LastProject = project
	c.progress.CompletedProjects++
	c.unflushed++
	if c.unflushed >= 100 || c.unflushedObjects >= 10000 || time.Since(c.lastFlush) >= 15*time.Second {
		p, err := c.Flush(ctx)
		return p, true, err
	}
	return c.progress, false, nil
}

func (c *projectInventory) Flush(ctx context.Context) (ports.InventoryProgress, error) {
	if c.closed || c.tx == nil {
		return c.progress, fmt.Errorf("checkpoint is closed: %w", domain.ErrConflict)
	}
	if _, err := c.tx.ExecContext(ctx, `UPDATE inventory_checkpoint SET
		completed_projects=?,last_project=?,scanned_objects=?,scanned_bytes=? WHERE id=1`,
		c.progress.CompletedProjects, c.progress.LastProject, c.progress.ScannedObjects, c.progress.ScannedBytes); err != nil {
		return c.progress, err
	}
	if err := c.tx.Commit(); err != nil {
		c.tx = nil
		return c.progress, err
	}
	c.tx = nil
	c.unflushed = 0
	c.unflushedObjects = 0
	c.lastFlush = time.Now()
	tx, err := c.db.BeginTx(ctx, nil)
	if err == nil {
		c.tx = tx
	}
	return c.progress, err
}

func (c *projectInventory) VerifyPages(ctx context.Context, verify func(string, int64, int64) error) error {
	if c.closed || c.tx == nil {
		return fmt.Errorf("checkpoint is closed: %w", domain.ErrConflict)
	}
	rows, err := c.tx.QueryContext(ctx, `SELECT project,size,mtime_ns FROM inventory_pages ORDER BY project`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err = ctx.Err(); err != nil {
			return err
		}
		var project string
		var size, mtimeNS int64
		if err = rows.Scan(&project, &size, &mtimeNS); err != nil {
			return err
		}
		if err = verify(project, size, mtimeNS); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (c *projectInventory) Stats(ctx context.Context) (domain.CatalogStats, error) {
	out := domain.CatalogStats{SourceID: c.sourceID}
	if c.closed || c.tx == nil {
		return out, fmt.Errorf("checkpoint is closed: %w", domain.ErrConflict)
	}
	err := c.tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size),0),COUNT(*) FROM catalog_entries WHERE source_id=?`, c.sourceID).Scan(&out.Bytes, &out.Objects)
	return out, err
}

func (c *projectInventory) Path() string { return c.path }

func (c *projectInventory) Close() error {
	if c.closed {
		return nil
	}
	if c.tx != nil {
		if _, err := c.Flush(context.Background()); err != nil {
			_ = c.Abort()
			return err
		}
		_ = c.tx.Rollback()
		c.tx = nil
	}
	c.closed = true
	return c.db.Close()
}

// Abort does NOT remove the checkpoint. Uncommitted projects are rolled back;
// completed groups survive a process stop and can be resumed.
func (c *projectInventory) Abort() error {
	if c.closed {
		return nil
	}
	c.closed = true
	if c.tx != nil {
		_ = c.tx.Rollback()
		c.tx = nil
	}
	return c.db.Close()
}

func (c *projectInventory) Discard() error {
	_ = c.Abort()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(c.path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (f *RebuildCatalogFactory) DiscardProjectInventory(jobID string) error {
	path, err := f.projectPath(jobID)
	if err != nil {
		return err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err = os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

var _ ports.ProjectInventorySink = (*projectInventory)(nil)
var _ ports.ProjectInventoryFactory = (*RebuildCatalogFactory)(nil)
