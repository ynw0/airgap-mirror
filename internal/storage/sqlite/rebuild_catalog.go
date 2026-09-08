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

	_ "modernc.org/sqlite"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

type RebuildCatalogFactory struct {
	Root string
}

func NewRebuildCatalogFactory(root string) *RebuildCatalogFactory {
	return &RebuildCatalogFactory{Root: root}
}

type rebuildCatalog struct {
	path     string
	sourceID string
	db       *sql.DB
	tx       *sql.Tx
	stmt     *sql.Stmt
	closed   bool
}

func (f *RebuildCatalogFactory) Create(ctx context.Context, jobID, sourceID string) (ports.CatalogBuildSink, error) {
	if strings.TrimSpace(f.Root) == "" || jobID == "" || sourceID == "" {
		return nil, fmt.Errorf("rebuild catalog root/job/source are required: %w", domain.ErrInvalid)
	}
	if strings.ContainsAny(jobID, `/\\`) {
		return nil, fmt.Errorf("unsafe maintenance job id %q: %w", jobID, domain.ErrInvalid)
	}
	root, err := filepath.Abs(f.Root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0750); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "catalog-"+jobID+".sqlite")
	if _, err = os.Stat(path); err == nil {
		return nil, fmt.Errorf("rebuild catalog already exists for job %s: %w", jobID, domain.ErrConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	cleanup := func() {
		_ = db.Close()
		_ = os.Remove(path)
	}
	if _, err = db.ExecContext(ctx, `PRAGMA journal_mode=OFF; PRAGMA synchronous=OFF; PRAGMA temp_store=MEMORY; CREATE TABLE catalog_entries(source_id TEXT NOT NULL,logical_path TEXT NOT NULL,size INTEGER NOT NULL,sha256 TEXT NOT NULL,package_key TEXT NOT NULL DEFAULT '',version TEXT NOT NULL DEFAULT '',attributes BLOB,updated_at TEXT NOT NULL,PRIMARY KEY(source_id,logical_path)) WITHOUT ROWID; CREATE INDEX ix_catalog_package ON catalog_entries(source_id,package_key);`); err != nil {
		cleanup()
		return nil, fmt.Errorf("initialize rebuild catalog: %w", err)
	}
	if err = os.Chmod(path, 0640); err != nil {
		cleanup()
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		cleanup()
		return nil, err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO catalog_entries(source_id,logical_path,size,sha256,package_key,version,attributes,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(source_id,logical_path) DO UPDATE SET size=excluded.size,sha256=excluded.sha256,package_key=excluded.package_key,version=excluded.version,attributes=excluded.attributes,updated_at=excluded.updated_at`)
	if err != nil {
		_ = tx.Rollback()
		cleanup()
		return nil, err
	}
	return &rebuildCatalog{path: path, sourceID: sourceID, db: db, tx: tx, stmt: stmt}, nil
}

func (c *rebuildCatalog) Put(ctx context.Context, entry domain.CatalogEntry) error {
	if c.closed {
		return fmt.Errorf("rebuild catalog is closed: %w", domain.ErrConflict)
	}
	if entry.SourceID != c.sourceID || entry.LogicalPath == "" || entry.Size < 0 || entry.SHA256 == "" {
		return fmt.Errorf("invalid rebuild catalog entry %q: %w", entry.LogicalPath, domain.ErrInvalid)
	}
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now()
	}
	_, err := c.stmt.ExecContext(ctx, entry.SourceID, entry.LogicalPath, entry.Size, strings.ToLower(entry.SHA256), entry.PackageKey, entry.Version, []byte(entry.Attributes), timeString(entry.UpdatedAt))
	return err
}

func (c *rebuildCatalog) Stats(ctx context.Context) (domain.CatalogStats, error) {
	stats := domain.CatalogStats{SourceID: c.sourceID}
	if c.closed {
		return stats, fmt.Errorf("rebuild catalog is closed: %w", domain.ErrConflict)
	}
	err := c.tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size),0),COUNT(*) FROM catalog_entries WHERE source_id=?`, c.sourceID).Scan(&stats.Bytes, &stats.Objects)
	return stats, err
}

func (c *rebuildCatalog) Path() string { return c.path }

func (c *rebuildCatalog) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	if c.stmt != nil {
		_ = c.stmt.Close()
	}
	if err := c.tx.Commit(); err != nil {
		_ = c.db.Close()
		return err
	}
	return c.db.Close()
}

func (c *rebuildCatalog) Abort() error {
	if !c.closed {
		c.closed = true
		if c.stmt != nil {
			_ = c.stmt.Close()
		}
		if c.tx != nil {
			_ = c.tx.Rollback()
		}
		if c.db != nil {
			_ = c.db.Close()
		}
	}
	removeErr := os.Remove(c.path)
	if errors.Is(removeErr, os.ErrNotExist) {
		return nil
	}
	return removeErr
}

var _ ports.CatalogBuildFactory = (*RebuildCatalogFactory)(nil)
var _ ports.CatalogBuildSink = (*rebuildCatalog)(nil)
