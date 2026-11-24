package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type CatalogSnapshot struct{ DB *sql.DB }

func OpenCatalogSnapshot(path string) (*sql.DB, *CatalogSnapshot, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	uriPath := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro&immutable=1"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	return db, &CatalogSnapshot{DB: db}, nil
}

func (s *CatalogSnapshot) Get(ctx context.Context, sourceID, logicalPath string) (domain.CatalogEntry, error) {
	var v domain.CatalogEntry
	var attrs []byte
	var updated string
	err := s.DB.QueryRowContext(ctx, `SELECT source_id,logical_path,size,sha256,package_key,version,attributes,updated_at FROM catalog_entries WHERE source_id=? AND logical_path=?`, sourceID, logicalPath).Scan(&v.SourceID, &v.LogicalPath, &v.Size, &v.SHA256, &v.PackageKey, &v.Version, &attrs, &updated)
	if err != nil {
		return v, mapNotFound(err)
	}
	v.Attributes = attrs
	v.UpdatedAt = parseTime(updated)
	return v, nil
}

func (s *CatalogSnapshot) ListByPackage(ctx context.Context, sourceID, key string) ([]domain.CatalogEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT source_id,logical_path,size,sha256,package_key,version,attributes,updated_at FROM catalog_entries WHERE source_id=? AND package_key=? ORDER BY logical_path`, sourceID, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CatalogEntry
	for rows.Next() {
		var v domain.CatalogEntry
		var attrs []byte
		var updated string
		if err = rows.Scan(&v.SourceID, &v.LogicalPath, &v.Size, &v.SHA256, &v.PackageKey, &v.Version, &attrs, &updated); err != nil {
			return nil, err
		}
		v.Attributes = attrs
		v.UpdatedAt = parseTime(updated)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (*CatalogSnapshot) Upsert(context.Context, domain.CatalogEntry) error {
	return fmt.Errorf("catalog snapshot is read-only: %w", domain.ErrInvalid)
}

func (*CatalogSnapshot) Delete(context.Context, string, string) error {
	return fmt.Errorf("catalog snapshot is read-only: %w", domain.ErrInvalid)
}

func (s *CatalogSnapshot) Stats(ctx context.Context, sourceID string) (domain.CatalogStats, error) {
	v := domain.CatalogStats{SourceID: sourceID}
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(size),0),COUNT(*) FROM catalog_entries WHERE source_id=?`, sourceID).Scan(&v.Bytes, &v.Objects)
	return v, err
}

func (s *CatalogSnapshot) ValidateSource(ctx context.Context, sourceID string) error {
	var wrong int64
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_entries WHERE source_id<>?`, sourceID).Scan(&wrong); err != nil {
		return err
	}
	if wrong != 0 {
		return fmt.Errorf("catalog snapshot contains %d rows for another source: %w", wrong, domain.ErrConflict)
	}
	return nil
}

func (s *CatalogSnapshot) WalkRepairProjects(ctx context.Context, sourceID string, visit func(string) error) error {
	var present int
	if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='repair_entries')`).Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		return nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT package_key FROM repair_entries WHERE source_id=? ORDER BY package_key`, sourceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var project string
		if err = rows.Scan(&project); err != nil {
			return err
		}
		if err = visit(project); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *CatalogSnapshot) RepairEntries(ctx context.Context, sourceID, project string) ([]domain.CatalogEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT logical_path,size,sha256 FROM repair_entries WHERE source_id=? AND package_key=? ORDER BY logical_path`, sourceID, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CatalogEntry
	for rows.Next() {
		v := domain.CatalogEntry{SourceID: sourceID, PackageKey: project}
		if err = rows.Scan(&v.LogicalPath, &v.Size, &v.SHA256); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *CatalogSnapshot) ValidateRepairSnapshot(ctx context.Context, sourceID string, repair bool) error {
	var present int
	if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='repair_entries')`).Scan(&present); err != nil {
		return err
	}
	if repair != (present == 1) {
		return fmt.Errorf("repair capsule schema and evidence disagree: %w", domain.ErrConflict)
	}
	if !repair {
		return nil
	}
	var count, invalid int64
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN r.source_id<>? OR r.size<0 OR LENGTH(r.sha256)<>64 OR r.package_key='' OR EXISTS(SELECT 1 FROM catalog_entries c WHERE c.source_id=r.source_id AND c.logical_path=r.logical_path) THEN 1 ELSE 0 END),0) FROM repair_entries r`, sourceID).Scan(&count, &invalid); err != nil {
		return err
	}
	if count == 0 || invalid != 0 {
		return fmt.Errorf("invalid repair evidence: %w", domain.ErrConflict)
	}
	return nil
}
