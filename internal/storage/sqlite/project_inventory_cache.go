package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

func inventoryCachePath(root, sourceID string) string {
	digest := sha256.Sum256([]byte(sourceID))
	return filepath.Join(root, "pypi-cache-"+hex.EncodeToString(digest[:])+".sqlite")
}

// Immutable read-only connections allow a later completed scan to atomically
// replace the cache filename without affecting existing readers.
func openInventoryCache(path string) (*sql.DB, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, domain.ErrInvalid
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", u.String())
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

func (c *projectInventory) closeCache() {
	if c.cache != nil {
		_ = c.cache.Close()
		c.cache = nil
	}
}

func (c *projectInventory) OpenPreviousCache(ctx context.Context, signature string) error {
	c.closeCache()
	db, err := openInventoryCache(c.cachePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var savedSource, savedSignature string
	var done, total int64
	err = db.QueryRowContext(ctx, "SELECT source_id,signature,completed_projects,total_projects FROM inventory_checkpoint WHERE id=1").Scan(&savedSource, &savedSignature, &done, &total)
	if err != nil {
		db.Close()
		return err
	}
	if savedSource != c.sourceID || savedSignature != signature || done != total {
		db.Close()
		return nil
	}
	var present int
	err = db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='inventory_reuse_pages')").Scan(&present)
	if err != nil || present != 1 {
		db.Close()
		return err
	}
	c.cache = db
	return nil
}

func (c *projectInventory) CachedProjectDigest(ctx context.Context, project string) (string, error) {
	if c.cache == nil {
		return "", nil
	}
	var digest string
	err := c.cache.QueryRowContext(ctx, "SELECT sha256 FROM inventory_reuse_pages WHERE project=?", project).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return digest, err
}

// Only projects whose complete set of rows belongs to that project are marked
// reusable. Cross-project duplicate references make the later project ineligible.
func (c *projectInventory) RecordProjectDigest(ctx context.Context, project, digest string) error {
	if c.projectUnsafe || c.projectRows == 0 {
		return nil
	}
	if len(digest) != 64 {
		return domain.ErrInvalid
	}
	_, err := c.tx.ExecContext(ctx, "INSERT INTO inventory_reuse_pages(project,sha256,entries) VALUES(?,?,?)", project, digest, c.projectRows)
	return err
}

func (c *projectInventory) WalkCachedProject(ctx context.Context, project, digest string, visit func(domain.CatalogEntry) error) error {
	if c.cache == nil {
		return domain.ErrConflict
	}
	var expected int64
	if err := c.cache.QueryRowContext(ctx, "SELECT entries FROM inventory_reuse_pages WHERE project=? AND sha256=?", project, digest).Scan(&expected); err != nil {
		return err
	}
	rows, err := c.cache.QueryContext(ctx, "SELECT logical_path,size,sha256,package_key,version,attributes,updated_at FROM catalog_entries WHERE source_id=? AND package_key=? ORDER BY logical_path", c.sourceID, project)
	if err != nil {
		return err
	}
	defer rows.Close()
	var count int64
	page := false
	for rows.Next() {
		var entry domain.CatalogEntry
		var updated string
		entry.SourceID = c.sourceID
		if err = rows.Scan(&entry.LogicalPath, &entry.Size, &entry.SHA256, &entry.PackageKey, &entry.Version, &entry.Attributes, &updated); err != nil {
			return err
		}
		entry.UpdatedAt = parseTime(updated)
		if entry.LogicalPath == "simple/"+project+"/index.html" && entry.SHA256 == digest {
			page = true
		}
		if err = visit(entry); err != nil {
			return err
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count != expected || !page {
		return fmt.Errorf("incomplete cached project %s: %w", project, domain.ErrConflict)
	}
	return nil
}

func (c *projectInventory) VerifyEntries(ctx context.Context, visit func(domain.CatalogEntry) error) error {
	rows, err := c.tx.QueryContext(ctx, "SELECT logical_path,size,sha256,package_key FROM catalog_entries WHERE source_id=? ORDER BY logical_path", c.sourceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		entry := domain.CatalogEntry{SourceID: c.sourceID}
		if err = rows.Scan(&entry.LogicalPath, &entry.Size, &entry.SHA256, &entry.PackageKey); err != nil {
			return err
		}
		if err = visit(entry); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Promotion is optional and happens only after successful Catalog activation.
// Never rename a database that still depends on its WAL or shared-memory file.
func (f *RebuildCatalogFactory) PromoteProjectInventory(jobID, sourceID string) error {
	path, err := f.projectPath(jobID)
	if err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err = os.Lstat(path+suffix); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return fmt.Errorf("inventory cache still has SQLite sidecar: %w", domain.ErrConflict)
			}
			return err
		}
	}
	db, err := openInventoryCache(path)
	if err != nil {
		return err
	}
	var saved, signature string
	var done, total int64
	err = db.QueryRow("SELECT source_id,signature,completed_projects,total_projects FROM inventory_checkpoint WHERE id=1").Scan(&saved, &signature, &done, &total)
	closeErr := db.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if saved != sourceID || signature == "" || done != total {
		return domain.ErrConflict
	}
	return os.Rename(path, inventoryCachePath(filepath.Dir(path), sourceID))
}

var _ ports.ProjectInventoryCache = (*projectInventory)(nil)
var _ ports.ProjectInventoryCacheFactory = (*RebuildCatalogFactory)(nil)
