package apt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

type publishFixtureCatalog struct {
	absentCatalog
	entry domain.CatalogEntry
}

func (c publishFixtureCatalog) Get(_ context.Context, source, logical string) (domain.CatalogEntry, error) {
	if c.entry.SourceID == source && c.entry.LogicalPath == logical {
		return c.entry, nil
	}
	return domain.CatalogEntry{}, domain.ErrNotFound
}

func TestOfflinePublicationVerifiesSignedProposedView(t *testing.T) {
	ctx := context.Background()
	deb := []byte("deb")
	index := []byte(fmt.Sprintf("Package: demo\nVersion: 1\nFilename: pool/demo.deb\nSize: 3\nSHA256: %s\n\n", shaHex(deb)))
	release := []byte(fmt.Sprintf("Suite: stable\nDate: %s\nSHA256:\n %s %d main/binary-amd64/Packages\n", time.Now().UTC().Format(time.RFC1123), shaHex(index), len(index)))
	keyring, sig, inline := signedReleaseFixture(t, release)
	for _, mode := range []string{"inrelease", "detached"} {
		t.Run(mode, func(t *testing.T) {
			base := "dists/stable/"
			files := map[string][]byte{"pool/demo.deb": deb, base + "main/binary-amd64/Packages": index}
			cursor := shaHex(inline)
			releaseName := base + "InRelease"
			if mode == "inrelease" {
				files[releaseName] = inline
			} else {
				releaseName = base + "Release"
				files[releaseName] = release
				files[base+"Release.gpg"] = sig
				cursor = shaHex([]byte(shaHex(release) + "\x00" + shaHex(sig)))
			}
			src := domain.Source{ID: "source", Type: domain.SourceAPT, Provider: Provider, ConfigJSON: json.RawMessage(fmt.Sprintf(`{"suite":"stable","releaseMode":%q,"trustedKeyringBase64":%q}`, mode, keyring))}
			unit := domain.PublishUnit{ID: "unit", SourceID: src.ID, Required: 2, Imported: 2}
			meta := domain.PublishMetadataEntry{SourceID: src.ID, PublishUnitID: unit.ID, LogicalPath: releaseName, Size: int64(len(files[releaseName])), SHA256: shaHex(files[releaseName]), Operation: domain.ArtifactUpdate}
			cat := publishFixtureCatalog{entry: domain.CatalogEntry{SourceID: src.ID, LogicalPath: "pool/demo.deb", Size: 3, SHA256: shaHex(deb)}}
			req := ports.PublishValidationRequest{Source: src, Unit: unit, Epoch: domain.Epoch{PublishUnitCount: 1, TargetCursor: domain.Cursor{Kind: "apt-release-snapshot-sha256", Value: cursor}}, Catalog: cat, Metadata: []domain.PublishMetadataEntry{meta}, OpenFile: func(logical string) (io.ReadCloser, int64, error) {
				b, ok := files[logical]
				if !ok {
					return nil, 0, os.ErrNotExist
				}
				return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
			}}
			if err := validateUpstreamPublication(ctx, req); err != nil {
				t.Fatal(err)
			}
			bad := req
			bad.Epoch.TargetCursor.Value = "forged"
			if err := validateUpstreamPublication(ctx, bad); err == nil {
				t.Fatal("forged cursor accepted")
			}
			bad = req
			bad.Metadata = append([]domain.PublishMetadataEntry{meta}, domain.PublishMetadataEntry{SourceID: src.ID, PublishUnitID: unit.ID, LogicalPath: base + "untrusted", Size: 1, SHA256: shaHex([]byte("x")), Operation: domain.ArtifactAdd})
			if err := validateUpstreamPublication(ctx, bad); err == nil {
				t.Fatal("unsigned metadata accepted")
			}
			bad = req
			bad.Catalog = publishFixtureCatalog{entry: domain.CatalogEntry{SourceID: src.ID, LogicalPath: "pool/demo.deb", Size: 3, SHA256: shaHex([]byte("bad"))}}
			if err := validateUpstreamPublication(ctx, bad); err == nil {
				t.Fatal("package different from signed index accepted")
			}
			files[base+"main/binary-amd64/Packages"] = append(append([]byte(nil), index...), 'x')
			if err := validateUpstreamPublication(ctx, req); err == nil {
				t.Fatal("tampered index accepted")
			}
			files[base+"main/binary-amd64/Packages"] = index
			delete(files, "pool/demo.deb")
			if err := validateUpstreamPublication(ctx, req); err == nil {
				t.Fatal("missing package accepted")
			}
		})
	}
}
