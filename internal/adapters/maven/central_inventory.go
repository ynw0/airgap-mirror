package maven

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/adapters/common"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/integrity"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

func reuseCentralSHA(ctx context.Context, current ports.CatalogStore, sourceID, logical string, size int64, upstreamSHA1 string) (string, bool, error) {
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
	if entry.Size != size || catalogCentralSHA1(entry) != strings.ToLower(upstreamSHA1) || !validHexDigest(strings.ToLower(entry.SHA256), 32) {
		return "", false, nil
	}
	return strings.ToLower(entry.SHA256), true, nil
}

func parseLocalSHA1(body []byte, logical string) (string, error) {
	fields := strings.Fields(string(body))
	if len(fields) != 1 {
		return "", fmt.Errorf("Maven Central checksum %s must contain one SHA-1 token: %w", logical, domain.ErrInvalid)
	}
	sum := strings.ToLower(fields[0])
	if !validHexDigest(sum, sha1.Size) {
		return "", fmt.Errorf("invalid Maven Central SHA-1 %q in %s: %w", fields[0], logical, domain.ErrInvalid)
	}
	return sum, nil
}

func (a *CentralAdapter) Inventory(ctx context.Context, req ports.InventoryRequest, sink ports.CatalogBuildSink) (domain.CatalogStats, error) {
	stats := domain.CatalogStats{SourceID: req.Source.ID}
	if req.Source.Type != domain.SourceMaven || req.Source.Provider != CentralProvider {
		return stats, fmt.Errorf("source must be maven/%s: %w", CentralProvider, domain.ErrInvalid)
	}
	cfg, err := parseCentralConfig(req.Source)
	if err != nil {
		return stats, err
	}
	repo, err := common.OpenLocalRepository(req.Source.RootPath)
	if err != nil {
		return stats, err
	}

	var scannedObjects, scannedBytes int64
	report := func(force bool) error {
		if req.Report == nil || (!force && scannedObjects%2048 != 0) {
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

	processed := map[string]bool{}
	for _, coordinate := range cfg.Coordinates {
		if err = ctx.Err(); err != nil {
			return stats, err
		}
		base := centralLogicalBase(coordinate)
		pom := path.Join(base, coordinate.ArtifactID+"-"+coordinate.Version+".pom")
		requested := path.Join(base, centralFileName(coordinate))
		for _, logical := range []string{pom, requested} {
			if processed[logical] {
				continue
			}
			processed[logical] = true
			sidecarLogical := logical + ".sha1"
			sidecar, sideInfo, err := repo.ReadFile(sidecarLogical, 4<<10)
			if err != nil {
				return stats, fmt.Errorf("read Maven Central checksum %s: %w", sidecarLogical, err)
			}
			upstreamSHA1, err := parseLocalSHA1(sidecar, sidecarLogical)
			if err != nil {
				return stats, err
			}
			resolved, info, err := repo.Resolve(logical)
			if err != nil {
				return stats, fmt.Errorf("Maven Central dependency-set missing %s: %w", logical, err)
			}
			sha, reused, err := reuseCentralSHA(ctx, req.Current, req.Source.ID, logical, info.Size(), upstreamSHA1)
			if err != nil {
				return stats, err
			}
			if !reused {
				sri, err := integrityFromHex("sha1", upstreamSHA1)
				if err != nil {
					return stats, err
				}
				sha, _, err = integrity.VerifyFile(resolved, sri)
				if err != nil {
					return stats, fmt.Errorf("verify Maven Central artifact %s: %w", logical, err)
				}
			}
			attrs, err := rawAttrs(centralAttrs{GroupID: coordinate.GroupID, ArtifactID: coordinate.ArtifactID, Version: coordinate.Version, Packaging: coordinate.Packaging, Classifier: coordinate.Classifier, UpstreamSHA1: upstreamSHA1})
			if err != nil {
				return stats, err
			}
			if err = put(domain.CatalogEntry{SourceID: req.Source.ID, LogicalPath: logical, Size: info.Size(), SHA256: sha, PackageKey: gavKey(coordinate), Version: coordinate.Version, Attributes: attrs, UpdatedAt: info.ModTime()}); err != nil {
				return stats, err
			}
			sideSum := sha256.Sum256(sidecar)
			if err = put(domain.CatalogEntry{SourceID: req.Source.ID, LogicalPath: sidecarLogical, Size: int64(len(sidecar)), SHA256: hex.EncodeToString(sideSum[:]), PackageKey: gavKey(coordinate), Version: coordinate.Version, Attributes: attrs, UpdatedAt: sideInfo.ModTime()}); err != nil {
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

var _ ports.InventoryAdapter = (*CentralAdapter)(nil)
