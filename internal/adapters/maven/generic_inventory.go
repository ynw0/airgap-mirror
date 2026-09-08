package maven

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/adapters/common"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/integrity"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

type genericInventoryConfig struct {
	InventoryMode string `json:"inventoryMode"`
}

func genericPackageFromPath(logical string) (string, string) {
	parts := strings.Split(logical, "/")
	if len(parts) < 4 {
		return "", ""
	}
	artifact := parts[len(parts)-3]
	version := parts[len(parts)-2]
	file := parts[len(parts)-1]
	if artifact == "" || version == "" || !strings.HasPrefix(file, artifact+"-"+version) {
		return "", ""
	}
	group := strings.Join(parts[:len(parts)-3], ".")
	if group == "" {
		return "", ""
	}
	return group + ":" + artifact, version
}

func reuseMavenSHA(ctx context.Context, current ports.CatalogStore, sourceID, logical string, size int64) (string, bool, error) {
	if current == nil {
		return "", false, nil
	}
	entry, err := current.Get(ctx, sourceID, logical)
	if errors.Is(err, domain.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if entry.Size != size || !validHexDigest(strings.ToLower(entry.SHA256), 32) {
		return "", false, nil
	}
	return strings.ToLower(entry.SHA256), true, nil
}

func (a *GenericAdapter) Inventory(ctx context.Context, req ports.InventoryRequest, sink ports.CatalogBuildSink) (domain.CatalogStats, error) {
	stats := domain.CatalogStats{SourceID: req.Source.ID}
	if req.Source.Type != domain.SourceMaven || req.Source.Provider != GenericProvider {
		return stats, fmt.Errorf("source must be maven/%s: %w", GenericProvider, domain.ErrInvalid)
	}
	if _, err := parseGenericConfig(req.Source); err != nil {
		return stats, err
	}
	var inventoryCfg genericInventoryConfig
	if err := json.Unmarshal(req.Source.ConfigJSON, &inventoryCfg); err != nil {
		return stats, fmt.Errorf("decode Maven generic inventory config: %w", err)
	}
	if inventoryCfg.InventoryMode != "filesystem" {
		return stats, fmt.Errorf("maven-generic inventory requires config inventoryMode=filesystem: %w", domain.ErrInvalid)
	}
	repo, err := common.OpenLocalRepository(req.Source.RootPath)
	if err != nil {
		return stats, err
	}

	var scannedObjects, scannedBytes int64
	report := func(force bool) error {
		if req.Report == nil || (!force && scannedObjects%4096 != 0) {
			return nil
		}
		return req.Report(ports.InventoryProgress{ScannedObjects: scannedObjects, ScannedBytes: scannedBytes})
	}

	err = filepath.WalkDir(repo.Root, func(filePath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if filePath == repo.Root || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(repo.Root, filePath)
		if err != nil {
			return err
		}
		logical := filepath.ToSlash(rel)
		resolved, info, err := repo.Resolve(logical)
		if err != nil {
			return fmt.Errorf("Maven generic inventory %s: %w", logical, err)
		}
		sha, reused, err := reuseMavenSHA(ctx, req.Current, req.Source.ID, logical, info.Size())
		if err != nil {
			return err
		}
		if !reused {
			sha, _, err = integrity.VerifyFile(resolved, "")
			if err != nil {
				return fmt.Errorf("hash Maven generic artifact %s: %w", logical, err)
			}
		}
		packageKey, version := genericPackageFromPath(logical)
		if err = sink.Put(ctx, domain.CatalogEntry{SourceID: req.Source.ID, LogicalPath: logical, Size: info.Size(), SHA256: sha, PackageKey: packageKey, Version: version, UpdatedAt: info.ModTime()}); err != nil {
			return err
		}
		scannedObjects++
		scannedBytes += info.Size()
		return report(false)
	})
	if err != nil {
		return stats, err
	}
	if err = report(true); err != nil {
		return stats, err
	}
	stats, err = sink.Stats(ctx)
	return stats, err
}

var _ ports.InventoryAdapter = (*GenericAdapter)(nil)
