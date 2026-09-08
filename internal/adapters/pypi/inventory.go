package pypi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"

	xhtml "golang.org/x/net/html"

	"github.com/ynw0/airgap-mirror/internal/adapters/common"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

type inventoryLink struct {
	LogicalPath    string
	SHA256         string
	RequiresPython string
	Yanked         *string
}

func parseInventoryLink(publicRoot *url.URL, pageURL *url.URL, attrs []xhtml.Attribute) (inventoryLink, bool, error) {
	var out inventoryLink
	var href string
	for _, attr := range attrs {
		switch strings.ToLower(attr.Key) {
		case "href":
			href = strings.TrimSpace(attr.Val)
		case "data-requires-python":
			out.RequiresPython = attr.Val
		case "data-yanked":
			reason := attr.Val
			out.Yanked = &reason
		}
	}
	if href == "" {
		return out, false, nil
	}
	u, err := url.Parse(href)
	if err != nil {
		return out, false, fmt.Errorf("parse PyPI inventory href %q: %w", href, err)
	}
	resolved := pageURL.ResolveReference(u)
	if resolved.Scheme != publicRoot.Scheme || !strings.EqualFold(resolved.Host, publicRoot.Host) {
		return out, false, fmt.Errorf("PyPI simple link points outside publicUrl: %s: %w", resolved.String(), domain.ErrConflict)
	}
	rootPath := strings.TrimRight(path.Clean(publicRoot.Path), "/")
	resolvedPath := path.Clean(resolved.Path)
	if rootPath == "." {
		rootPath = ""
	}
	if rootPath != "" {
		if resolvedPath != rootPath && !strings.HasPrefix(resolvedPath, rootPath+"/") {
			return out, false, fmt.Errorf("PyPI simple link %s is outside publicUrl path %s: %w", resolvedPath, rootPath, domain.ErrConflict)
		}
		resolvedPath = strings.TrimPrefix(resolvedPath, rootPath)
	}
	logical := strings.TrimPrefix(path.Clean(resolvedPath), "/")
	if logical == "." || !strings.HasPrefix(logical, "packages/") {
		return out, false, fmt.Errorf("PyPI simple link does not point to packages/: %s: %w", logical, domain.ErrInvalid)
	}
	fragment, err := url.ParseQuery(resolved.Fragment)
	if err != nil {
		return out, false, fmt.Errorf("parse PyPI hash fragment %q: %w", resolved.Fragment, err)
	}
	sum := strings.ToLower(strings.TrimSpace(fragment.Get("sha256")))
	if len(sum) != 64 {
		return out, false, fmt.Errorf("PyPI simple link %s is missing sha256 fragment: %w", logical, domain.ErrInvalid)
	}
	if _, err = hex.DecodeString(sum); err != nil {
		return out, false, fmt.Errorf("PyPI simple link %s has invalid sha256: %w", logical, domain.ErrInvalid)
	}
	out.LogicalPath = logical
	out.SHA256 = sum
	return out, true, nil
}

func (a *Adapter) Inventory(ctx context.Context, req ports.InventoryRequest, sink ports.CatalogBuildSink) (domain.CatalogStats, error) {
	stats := domain.CatalogStats{SourceID: req.Source.ID}
	if req.Source.Type != domain.SourcePyPI || req.Source.Provider != Provider {
		return stats, fmt.Errorf("source must be pypi/%s: %w", Provider, domain.ErrInvalid)
	}
	repo, err := common.OpenLocalRepository(req.Source.RootPath)
	if err != nil {
		return stats, err
	}
	publicRoot, err := url.Parse(req.Source.PublicURL)
	if err != nil || publicRoot.Scheme == "" || publicRoot.Host == "" {
		return stats, fmt.Errorf("PyPI publicUrl is invalid: %w", domain.ErrInvalid)
	}
	simpleDir, err := repo.ResolveDirectory("simple")
	if err != nil {
		return stats, fmt.Errorf("resolve PyPI simple directory: %w", err)
	}
	projects, err := os.ReadDir(simpleDir)
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

	for _, projectEntry := range projects {
		if err = ctx.Err(); err != nil {
			return stats, err
		}
		if !projectEntry.IsDir() {
			continue
		}
		project := normalizeProject(projectEntry.Name())
		if project == "" || project != projectEntry.Name() {
			return stats, fmt.Errorf("PyPI simple directory %q is not normalized: %w", projectEntry.Name(), domain.ErrConflict)
		}
		pageLogical := path.Join("simple", project, "index.html")
		f, pageInfo, err := repo.Open(pageLogical)
		if err != nil {
			return stats, fmt.Errorf("open PyPI project page %s: %w", project, err)
		}
		pageURL, err := url.Parse(strings.TrimRight(req.Source.PublicURL, "/") + "/simple/" + url.PathEscape(project) + "/index.html")
		if err != nil {
			f.Close()
			return stats, err
		}
		h := sha256.New()
		z := xhtml.NewTokenizer(io.TeeReader(f, h))
		for {
			tt := z.Next()
			if tt == xhtml.ErrorToken {
				if z.Err() != io.EOF {
					f.Close()
					return stats, fmt.Errorf("parse PyPI project page %s: %w", project, z.Err())
				}
				break
			}
			if tt != xhtml.StartTagToken && tt != xhtml.SelfClosingTagToken {
				continue
			}
			tok := z.Token()
			if !strings.EqualFold(tok.Data, "a") {
				continue
			}
			link, ok, parseErr := parseInventoryLink(publicRoot, pageURL, tok.Attr)
			if parseErr != nil {
				f.Close()
				return stats, fmt.Errorf("PyPI project %s: %w", project, parseErr)
			}
			if !ok {
				continue
			}
			_, artifactInfo, statErr := repo.Resolve(link.LogicalPath)
			if statErr != nil {
				f.Close()
				return stats, fmt.Errorf("PyPI project %s references missing %s: %w", project, link.LogicalPath, statErr)
			}
			attrs := map[string]any{}
			if link.RequiresPython != "" {
				attrs["requiresPython"] = link.RequiresPython
			}
			if link.Yanked != nil {
				attrs["yanked"] = *link.Yanked
			}
			var raw json.RawMessage
			if len(attrs) != 0 {
				encoded, marshalErr := json.Marshal(attrs)
				if marshalErr != nil {
					f.Close()
					return stats, marshalErr
				}
				raw = encoded
			}
			if err = put(domain.CatalogEntry{SourceID: req.Source.ID, LogicalPath: link.LogicalPath, Size: artifactInfo.Size(), SHA256: link.SHA256, PackageKey: project, Attributes: raw, UpdatedAt: artifactInfo.ModTime()}); err != nil {
				f.Close()
				return stats, err
			}
		}
		if err = f.Close(); err != nil {
			return stats, err
		}
		pageSHA := hex.EncodeToString(h.Sum(nil))
		if err = put(domain.CatalogEntry{SourceID: req.Source.ID, LogicalPath: pageLogical, Size: pageInfo.Size(), SHA256: pageSHA, PackageKey: project, UpdatedAt: pageInfo.ModTime()}); err != nil {
			return stats, err
		}
	}
	if err = report(true); err != nil {
		return stats, err
	}
	stats, err = sink.Stats(ctx)
	return stats, err
}

var _ ports.InventoryAdapter = (*Adapter)(nil)
