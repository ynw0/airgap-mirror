package apt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ynw0/airgap-mirror/internal/adapters/common"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

func (a *Adapter) Inventory(ctx context.Context, req ports.InventoryRequest, sink ports.CatalogBuildSink) (domain.CatalogStats, error) {
	stats := domain.CatalogStats{SourceID: req.Source.ID}
	if req.Source.Type != domain.SourceAPT || req.Source.Provider != Provider {
		return stats, fmt.Errorf("source must be apt/%s: %w", Provider, domain.ErrInvalid)
	}
	cfg, err := parseConfig(req.Source)
	if err != nil {
		return stats, err
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
	put := func(logical string, size int64, sum, packageKey, version string, updated time.Time) error {
		if err := sink.Put(ctx, domain.CatalogEntry{SourceID: req.Source.ID, LogicalPath: logical, Size: size, SHA256: strings.ToLower(sum), PackageKey: packageKey, Version: version, UpdatedAt: updated}); err != nil {
			return err
		}
		scannedObjects++
		scannedBytes += size
		return report(false)
	}
	verifyStat := func(logical string, size int64) (time.Time, error) {
		_, info, err := repo.Resolve(logical)
		if err != nil {
			return time.Time{}, fmt.Errorf("APT inventory missing %s: %w", logical, err)
		}
		if info.Size() != size {
			return time.Time{}, fmt.Errorf("APT inventory size mismatch %s expected=%d actual=%d: %w", logical, size, info.Size(), domain.ErrConflict)
		}
		return info.ModTime(), nil
	}

	distBase := path.Join("dists", cfg.Suite)
	var plain []byte
	if cfg.ReleaseMode == "inrelease" {
		logical := path.Join(distBase, "InRelease")
		body, info, err := repo.ReadFile(logical, 32<<20)
		if err != nil {
			return stats, err
		}
		plain, err = clearSignedBody(body)
		if err != nil {
			return stats, err
		}
		if err = put(logical, int64(len(body)), shaHex(body), "", "", info.ModTime()); err != nil {
			return stats, err
		}
	} else {
		releaseLogical := path.Join(distBase, "Release")
		body, info, err := repo.ReadFile(releaseLogical, 32<<20)
		if err != nil {
			return stats, err
		}
		plain = body
		if err = put(releaseLogical, int64(len(body)), shaHex(body), "", "", info.ModTime()); err != nil {
			return stats, err
		}
		sigLogical := path.Join(distBase, "Release.gpg")
		sig, sigInfo, err := repo.ReadFile(sigLogical, 8<<20)
		if err != nil {
			return stats, err
		}
		if err = put(sigLogical, int64(len(sig)), shaHex(sig), "", "", sigInfo.ModTime()); err != nil {
			return stats, err
		}
	}

	doc, err := parseRelease(plain)
	if err != nil {
		return stats, err
	}
	parseIndexes := pickParseIndexes(doc.Entries)
	parseSet := make(map[string]struct{}, len(parseIndexes))
	for _, index := range parseIndexes {
		parseSet[index.Path] = struct{}{}
	}

	for _, entry := range doc.Entries {
		logical := path.Join(distBase, entry.Path)
		updated, err := verifyStat(logical, entry.Size)
		if err != nil {
			return stats, err
		}
		if _, parsedLater := parseSet[entry.Path]; !parsedLater {
			sum, n, _, hashErr := repo.SHA256(logical)
			if hashErr != nil {
				return stats, hashErr
			}
			if n != entry.Size || !strings.EqualFold(sum, entry.SHA256) {
				return stats, fmt.Errorf("APT metadata hash mismatch %s: %w", logical, domain.ErrConflict)
			}
		}
		if err = put(logical, entry.Size, entry.SHA256, "", "", updated); err != nil {
			return stats, err
		}
		if doc.AcquireByHash {
			byHash := path.Join(path.Dir(logical), "by-hash", "SHA256", entry.SHA256)
			byHashUpdated, statErr := verifyStat(byHash, entry.Size)
			if statErr != nil {
				return stats, statErr
			}
			if err = put(byHash, entry.Size, entry.SHA256, "", "", byHashUpdated); err != nil {
				return stats, err
			}
		}
	}

	for _, index := range parseIndexes {
		logical := path.Join(distBase, index.Path)
		f, _, err := repo.Open(logical)
		if err != nil {
			return stats, err
		}
		h := sha256.New()
		tee := io.TeeReader(f, h)
		decoded, closer, err := decompressor(index.Path, tee)
		if err != nil {
			f.Close()
			return stats, err
		}
		_, pkgIndex, sourceIndex := baseIndexName(index.Path)
		err = parseDeb822(decoded, func(rec map[string]string) error {
			if pkgIndex {
				filename := rec["Filename"]
				sizeText := rec["Size"]
				sum := strings.ToLower(rec["SHA256"])
				if filename == "" || sizeText == "" || !validSHA256(sum) {
					return fmt.Errorf("Packages record missing Filename/Size/SHA256: %w", domain.ErrInvalid)
				}
				size, parseErr := strconv.ParseInt(sizeText, 10, 64)
				if parseErr != nil || size < 0 {
					return domain.ErrInvalid
				}
				logicalPath := path.Clean(filename)
				if logicalPath != filename || strings.HasPrefix(logicalPath, "../") || strings.HasPrefix(logicalPath, "/") {
					return domain.ErrInvalid
				}
				updated, statErr := verifyStat(logicalPath, size)
				if statErr != nil {
					return statErr
				}
				return put(logicalPath, size, sum, rec["Package"], rec["Version"], updated)
			}
			if sourceIndex {
				dir := rec["Directory"]
				checks := rec["Checksums-Sha256"]
				if dir == "" || checks == "" {
					return fmt.Errorf("Sources record missing Directory/Checksums-Sha256: %w", domain.ErrInvalid)
				}
				for _, line := range strings.Split(checks, "\n") {
					fields := strings.Fields(line)
					if len(fields) != 3 || !validSHA256(fields[0]) {
						return domain.ErrInvalid
					}
					size, parseErr := strconv.ParseInt(fields[1], 10, 64)
					if parseErr != nil || size < 0 {
						return domain.ErrInvalid
					}
					logicalPath := path.Clean(path.Join(dir, fields[2]))
					if strings.HasPrefix(logicalPath, "../") || strings.HasPrefix(logicalPath, "/") {
						return domain.ErrInvalid
					}
					updated, statErr := verifyStat(logicalPath, size)
					if statErr != nil {
						return statErr
					}
					if putErr := put(logicalPath, size, strings.ToLower(fields[0]), rec["Package"], rec["Version"], updated); putErr != nil {
						return putErr
					}
				}
			}
			return nil
		})
		if closer != nil {
			_ = closer.Close()
		}
		_, _ = io.Copy(io.Discard, tee)
		closeErr := f.Close()
		if err != nil {
			return stats, err
		}
		if closeErr != nil {
			return stats, closeErr
		}
		if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), index.SHA256) {
			return stats, fmt.Errorf("APT index %s sha256 mismatch: %w", index.Path, domain.ErrConflict)
		}
	}

	if err = report(true); err != nil {
		return stats, err
	}
	stats, err = sink.Stats(ctx)
	return stats, err
}

var _ ports.InventoryAdapter = (*Adapter)(nil)
