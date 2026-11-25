package apt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

// validateUpstreamPublication independently verifies the signed proposed
// metadata view on the offline Agent before the first metadata rename.
func validateUpstreamPublication(ctx context.Context, req ports.PublishValidationRequest) error {
	if req.Unit.Imported != req.Unit.Required || req.Epoch.PublishUnitCount != 1 || req.OpenFile == nil || req.Catalog == nil {
		return fmt.Errorf("APT 发布必须提供单一完整的发布单元及待发布文件: %w", domain.ErrConflict)
	}
	cfg, err := parseConfig(req.Source)
	if err != nil {
		return err
	}
	keys, err := decodeTrustedKeyring(cfg.TrustedKeyringBase64)
	if err != nil {
		return err
	}
	base := path.Join("dists", cfg.Suite)
	declared := make(map[string]domain.PublishMetadataEntry, len(req.Metadata))
	for _, m := range req.Metadata {
		if m.SourceID != req.Source.ID || m.PublishUnitID != req.Unit.ID || m.Size < 0 || !validSHA256(m.SHA256) ||
			(m.Operation != domain.ArtifactAdd && m.Operation != domain.ArtifactUpdate) {
			return fmt.Errorf("无效的 APT 元数据声明 %s: %w", m.LogicalPath, domain.ErrConflict)
		}
		if _, exists := declared[m.LogicalPath]; exists {
			return fmt.Errorf("重复的 APT 元数据 %s: %w", m.LogicalPath, domain.ErrConflict)
		}
		declared[m.LogicalPath] = m
	}
	checkDeclaration := func(logical string, size int64, digest string) error {
		if m, ok := declared[logical]; ok {
			if m.Size != size || m.SHA256 != digest {
				return fmt.Errorf("APT 元数据声明与可信内容不一致 %s: %w", logical, domain.ErrConflict)
			}
			delete(declared, logical)
		}
		return nil
	}
	readRelease := func(logical string, max int64) ([]byte, error) {
		f, size, err := req.OpenFile(logical)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if size <= 0 || size > max {
			return nil, domain.ErrInvalid
		}
		body, err := io.ReadAll(io.LimitReader(f, max+1))
		if err != nil {
			return nil, err
		}
		if int64(len(body)) != size {
			return nil, domain.ErrConflict
		}
		return body, checkDeclaration(logical, size, shaHex(body))
	}
	var release, signature []byte
	if cfg.ReleaseMode == "inrelease" {
		release, err = readRelease(path.Join(base, "InRelease"), 32<<20)
	} else {
		release, err = readRelease(path.Join(base, "Release"), 32<<20)
		if err == nil {
			signature, err = readRelease(path.Join(base, "Release.gpg"), 8<<20)
		}
	}
	if err != nil {
		return fmt.Errorf("读取待发布 APT 签名文件: %w", err)
	}
	plain, err := verifyUpstreamRelease(ctx, keys, release, signature, cfg.ReleaseMode == "inrelease")
	if err != nil {
		return err
	}
	if err = validateReleaseFreshness(plain, cfg, time.Now()); err != nil {
		return err
	}
	cursor := shaHex(release)
	if cfg.ReleaseMode == "detached" {
		cursor = shaHex([]byte(cursor + "\x00" + shaHex(signature)))
	}
	if !req.Epoch.TargetCursor.Equal(domain.Cursor{Kind: "apt-release-snapshot-sha256", Value: cursor}) {
		return fmt.Errorf("APT 同步位置与已验证的 Release 不一致: %w", domain.ErrConflict)
	}
	doc, err := parseRelease(plain)
	if err != nil {
		return err
	}
	// Every named index is authenticated by Release. Parse the preferred
	// Packages/Sources representation while hashing the actual compressed file.
	selected := make(map[string]bool)
	for _, e := range pickParseIndexes(doc.Entries) {
		selected[e.Path] = true
	}
	seen := make(map[string]bool)
	verifyPackage := func(logical string, size int64, digest string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !strings.HasPrefix(logical, "pool/") || pack.ValidateLogicalPath(logical) != nil || size < 0 || !validSHA256(digest) {
			return domain.ErrInvalid
		}
		entry, err := req.Catalog.Get(ctx, req.Source.ID, logical)
		if err != nil || entry.Size != size || !strings.EqualFold(entry.SHA256, digest) {
			return fmt.Errorf("APT 软件包与签名索引不一致 %s: %w", logical, domain.ErrConflict)
		}
		f, actual, err := req.OpenFile(logical)
		if err != nil {
			return fmt.Errorf("APT 签名索引引用的软件包不可用 %s: %w", logical, err)
		}
		closeErr := f.Close()
		if actual != size {
			return fmt.Errorf("APT 软件包大小不一致 %s: %w", logical, domain.ErrConflict)
		}
		return closeErr
	}
	verifyIndex := func(logical string, entry releaseEntry, parse bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Size == math.MaxInt64 {
			return domain.ErrInvalid
		}
		if err := checkDeclaration(logical, entry.Size, entry.SHA256); err != nil {
			return err
		}
		f, size, err := req.OpenFile(logical)
		if err != nil {
			return err
		}
		defer f.Close()
		if size != entry.Size {
			return fmt.Errorf("APT 索引大小不一致 %s: %w", logical, domain.ErrConflict)
		}
		h := sha256.New()
		r := io.TeeReader(io.LimitReader(f, entry.Size+1), h)
		if parse {
			decoded, closer, err := decompressor(entry.Path, r)
			if err != nil {
				return err
			}
			_, pkg, src := baseIndexName(entry.Path)
			err = parseDeb822(decoded, func(rec map[string]string) error {
				if pkg {
					n, e := strconv.ParseInt(rec["Size"], 10, 64)
					if e != nil {
						return e
					}
					return verifyPackage(rec["Filename"], n, rec["SHA256"])
				}
				if src {
					if rec["Directory"] == "" || rec["Checksums-Sha256"] == "" {
						return domain.ErrInvalid
					}
					for _, line := range strings.Split(rec["Checksums-Sha256"], "\n") {
						fields := strings.Fields(line)
						if len(fields) != 3 || path.Base(fields[2]) != fields[2] || fields[2] == "." || fields[2] == ".." {
							return domain.ErrInvalid
						}
						n, e := strconv.ParseInt(fields[1], 10, 64)
						if e != nil {
							return e
						}
						if e = verifyPackage(path.Join(rec["Directory"], fields[2]), n, fields[0]); e != nil {
							return e
						}
					}
				}
				return nil
			})
			if closer != nil {
				_ = closer.Close()
			}
			if err != nil {
				return err
			}
		}
		if _, err = io.Copy(io.Discard, r); err != nil {
			return err
		}
		if hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
			return fmt.Errorf("APT 索引 SHA256 不一致 %s: %w", logical, domain.ErrConflict)
		}
		return nil
	}
	for _, entry := range doc.Entries {
		logical := path.Join(base, entry.Path)
		if seen[logical] {
			return fmt.Errorf("Release 重复声明索引 %s: %w", logical, domain.ErrConflict)
		}
		seen[logical] = true
		if err = verifyIndex(logical, entry, selected[entry.Path]); err != nil {
			return err
		}
		if doc.AcquireByHash {
			byHash := path.Join(path.Dir(logical), "by-hash", "SHA256", entry.SHA256)
			if !seen[byHash] {
				seen[byHash] = true
				if err = verifyIndex(byHash, entry, false); err != nil {
					return err
				}
			}
		}
	}
	if len(declared) != 0 {
		return fmt.Errorf("离线包包含上游 Release 未授权的元数据: %w", domain.ErrConflict)
	}
	return nil
}
