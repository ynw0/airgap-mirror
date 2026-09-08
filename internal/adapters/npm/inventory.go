package npm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/adapters/common"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/integrity"
	"github.com/ynw0/airgap-mirror/internal/pack"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

func inventoryTarballLogical(publicBase, rawURL string) (string, error) {
	root, err := url.Parse(publicBase)
	if err != nil || root.Scheme == "" || root.Host == "" {
		return "", fmt.Errorf("invalid npm publicUrl: %w", domain.ErrInvalid)
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid npm mirrored tarball URL %q: %w", rawURL, domain.ErrInvalid)
	}
	if u.Scheme != root.Scheme || !strings.EqualFold(u.Host, root.Host) {
		return "", fmt.Errorf("npm mirrored tarball %s is outside publicUrl host: %w", rawURL, domain.ErrConflict)
	}
	rootPath := strings.TrimRight(path.Clean(root.Path), "/")
	resolvedPath := path.Clean(u.Path)
	if rootPath == "." {
		rootPath = ""
	}
	if rootPath != "" {
		if resolvedPath != rootPath && !strings.HasPrefix(resolvedPath, rootPath+"/") {
			return "", fmt.Errorf("npm mirrored tarball %s is outside publicUrl path: %w", rawURL, domain.ErrConflict)
		}
		resolvedPath = strings.TrimPrefix(resolvedPath, rootPath)
	}
	logical := strings.TrimPrefix(resolvedPath, "/")
	if !strings.HasSuffix(strings.ToLower(logical), ".tgz") {
		return "", fmt.Errorf("npm mirrored tarball path %s is not .tgz: %w", logical, domain.ErrInvalid)
	}
	if err = pack.ValidateLogicalPath(logical); err != nil {
		return "", err
	}
	return logical, nil
}

func validCatalogSHA256(sum string) bool {
	if len(sum) != 64 {
		return false
	}
	_, err := hex.DecodeString(sum)
	return err == nil
}

func reuseNPMCatalogSHA(ctx context.Context, current ports.CatalogStore, sourceID, logical string, size int64, upstreamIntegrity string) (string, bool, error) {
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
	oldIntegrity, _ := catalogIntegrity(entry)
	if entry.Size != size || oldIntegrity != upstreamIntegrity || !validCatalogSHA256(strings.ToLower(entry.SHA256)) {
		return "", false, nil
	}
	return strings.ToLower(entry.SHA256), true, nil
}

func (a *Adapter) Inventory(ctx context.Context, req ports.InventoryRequest, sink ports.CatalogBuildSink) (domain.CatalogStats, error) {
	stats := domain.CatalogStats{SourceID: req.Source.ID}
	if req.Source.Type != domain.SourceNPM || req.Source.Provider != Provider {
		return stats, fmt.Errorf("source must be npm/%s: %w", Provider, domain.ErrInvalid)
	}
	if _, err := parseConfig(req.Source); err != nil {
		return stats, err
	}
	repo, err := common.OpenLocalRepository(req.Source.RootPath)
	if err != nil {
		return stats, err
	}
	packumentDir, err := repo.ResolveDirectory("packuments")
	if err != nil {
		return stats, fmt.Errorf("resolve npm packuments directory: %w", err)
	}
	entries, err := os.ReadDir(packumentDir)
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
	put := func(entry domain.CatalogEntry) error {
		if err := sink.Put(ctx, entry); err != nil {
			return err
		}
		scannedObjects++
		scannedBytes += entry.Size
		return report(false)
	}

	for _, dirEntry := range entries {
		if err = ctx.Err(); err != nil {
			return stats, err
		}
		if dirEntry.IsDir() || !strings.HasSuffix(strings.ToLower(dirEntry.Name()), ".json") {
			continue
		}
		escaped := strings.TrimSuffix(dirEntry.Name(), ".json")
		name, err := url.PathUnescape(escaped)
		if err != nil || name == "" {
			return stats, fmt.Errorf("decode npm packument filename %q: %w", dirEntry.Name(), domain.ErrInvalid)
		}
		logical := packumentLogicalPath(name)
		if logical != path.Join("packuments", dirEntry.Name()) {
			return stats, fmt.Errorf("npm packument filename is not canonical for %q: %w", name, domain.ErrConflict)
		}
		body, info, err := repo.ReadFile(logical, 256<<20)
		if err != nil {
			return stats, err
		}
		doc, err := decodePackument(body)
		if err != nil {
			return stats, fmt.Errorf("decode npm packument %s: %w", name, err)
		}
		actualName, ok := stringField(doc, "name")
		if !ok || actualName != name {
			return stats, fmt.Errorf("npm packument name %q does not match file %q: %w", actualName, name, domain.ErrConflict)
		}
		actualRev, ok := stringField(doc, "_rev")
		if !ok || actualRev == "" {
			return stats, fmt.Errorf("npm packument %s missing _rev: %w", name, domain.ErrInvalid)
		}
		metaAttrs, err := attrsJSON(artifactAttrs{Package: name, UpstreamRev: actualRev})
		if err != nil {
			return stats, err
		}
		metaHash := sha256Hex(body)
		if err = put(domain.CatalogEntry{SourceID: req.Source.ID, LogicalPath: logical, Size: int64(len(body)), SHA256: metaHash, PackageKey: name, Attributes: metaAttrs, UpdatedAt: info.ModTime()}); err != nil {
			return stats, err
		}

		versionsRaw, exists := doc["versions"]
		if !exists {
			continue
		}
		versions, ok := versionsRaw.(map[string]any)
		if !ok {
			return stats, fmt.Errorf("npm packument %s versions is not an object: %w", name, domain.ErrInvalid)
		}
		keys := make([]string, 0, len(versions))
		for version := range versions {
			keys = append(keys, version)
		}
		sort.Strings(keys)
		for _, version := range keys {
			versionDoc, ok := versions[version].(map[string]any)
			if !ok {
				return stats, fmt.Errorf("npm %s@%s metadata is not an object: %w", name, version, domain.ErrInvalid)
			}
			distRaw, exists := versionDoc["dist"]
			if !exists {
				continue
			}
			dist, ok := distRaw.(map[string]any)
			if !ok {
				return stats, fmt.Errorf("npm %s@%s dist is not an object: %w", name, version, domain.ErrInvalid)
			}
			tarball, ok := stringField(dist, "tarball")
			if !ok || tarball == "" {
				return stats, fmt.Errorf("npm %s@%s dist.tarball missing: %w", name, version, domain.ErrInvalid)
			}
			upstreamIntegrity, err := normalizeIntegrity(dist)
			if err != nil {
				return stats, fmt.Errorf("npm %s@%s: %w", name, version, err)
			}
			tarballLogical, err := inventoryTarballLogical(req.Source.PublicURL, tarball)
			if err != nil {
				return stats, fmt.Errorf("npm %s@%s: %w", name, version, err)
			}
			tarballPath, tarballInfo, err := repo.Resolve(tarballLogical)
			if err != nil {
				return stats, fmt.Errorf("npm %s@%s references missing %s: %w", name, version, tarballLogical, err)
			}
			sha, reused, err := reuseNPMCatalogSHA(ctx, req.Current, req.Source.ID, tarballLogical, tarballInfo.Size(), upstreamIntegrity)
			if err != nil {
				return stats, err
			}
			if !reused {
				sha, _, err = integrity.VerifyFile(tarballPath, upstreamIntegrity)
				if err != nil {
					return stats, fmt.Errorf("verify npm %s@%s tarball: %w", name, version, err)
				}
			}
			attrs, err := attrsJSON(artifactAttrs{Package: name, Version: version, UpstreamRev: actualRev, UpstreamIntegrity: upstreamIntegrity})
			if err != nil {
				return stats, err
			}
			if err = put(domain.CatalogEntry{SourceID: req.Source.ID, LogicalPath: tarballLogical, Size: tarballInfo.Size(), SHA256: sha, PackageKey: name, Version: version, Attributes: attrs, UpdatedAt: tarballInfo.ModTime()}); err != nil {
				return stats, err
			}
		}
	}
	if err = report(true); err != nil {
		return stats, err
	}
	stats, err = sink.Stats(ctx)
	return stats, err
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

var _ ports.InventoryAdapter = (*Adapter)(nil)
