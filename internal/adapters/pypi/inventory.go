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
	var resumable ports.ProjectInventorySink
	cache, _ := sink.(ports.ProjectInventoryCache)
	var starting ports.InventoryProgress
	if sink, ok := sink.(ports.ProjectInventorySink); ok {
		resumable = sink
		names := sha256.New()
		var total int64
		for _, entry := range projects {
			if !entry.IsDir() {
				continue
			}
			project := normalizeProject(entry.Name())
			if project == "" || project != entry.Name() {
				return stats, fmt.Errorf("PyPI simple directory %q is not normalized: %w", entry.Name(), domain.ErrConflict)
			}
			_, _ = names.Write([]byte(project + "\n"))
			total++
		}
		signature := sha256.Sum256([]byte(strings.Join([]string{
			req.Source.ID, string(req.Source.Type), req.Source.Provider,
			req.Source.RootPath, req.Source.PublicURL, string(req.Source.ConfigJSON),
		}, "\x00")))
		sourceSignature := hex.EncodeToString(signature[:])
		starting, err = resumable.Restore(ctx, sourceSignature, hex.EncodeToString(names.Sum(nil)), total)
		if err != nil {
			return stats, err
		}
		if req.Incremental && cache != nil {
			if err = cache.OpenPreviousCache(ctx, sourceSignature); err != nil {
				return stats, fmt.Errorf("open previous PyPI inventory cache: %w", err)
			}
		}
		if req.Report != nil {
			if err = req.Report(starting); err != nil {
				return stats, err
			}
		}
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
		if resumable != nil {
			return nil // durable counts come from the SQLite project checkpoint
		}
		scannedObjects++
		scannedBytes += entry.Size
		return report(false)
	}

	completeProject := func(project, digest string, info os.FileInfo) error {
		if resumable == nil {
			return nil
		}
		if cache != nil {
			if err := cache.RecordProjectDigest(ctx, project, digest); err != nil {
				return err
			}
		}
		progress, durable, err := resumable.CheckpointProject(ctx, project, info.Size(), info.ModTime().UnixNano())
		if err != nil {
			return err
		}
		if durable && req.Report != nil {
			return req.Report(progress)
		}
		return nil
	}

	for _, projectEntry := range projects {
		if err = ctx.Err(); err != nil {
			return stats, err
		}
		if !projectEntry.IsDir() {
			continue
		}
		project := normalizeProject(projectEntry.Name())
		if resumable != nil && project <= starting.LastProject {
			continue
		}
		if project == "" || project != projectEntry.Name() {
			return stats, fmt.Errorf("PyPI simple directory %q is not normalized: %w", projectEntry.Name(), domain.ErrConflict)
		}
		pageLogical := path.Join("simple", project, "index.html")
		f, pageInfo, err := repo.Open(pageLogical)
		if err != nil {
			return stats, fmt.Errorf("open PyPI project page %s: %w", project, err)
		}
		if req.Incremental && cache != nil {
			cachedDigest, cacheErr := cache.CachedProjectDigest(ctx, project)
			if cacheErr != nil {
				f.Close()
				return stats, cacheErr
			}
			if cachedDigest != "" {
				digest, hashErr := inventoryPageDigest(ctx, f)
				if hashErr != nil {
					f.Close()
					return stats, hashErr
				}
				if digest == cachedDigest {
					// Exact page content, not merely mtime, establishes the
					// reference set. Every referenced file is still statted.
					if err = cache.WalkCachedProject(ctx, project, digest, func(entry domain.CatalogEntry) error {
						if err := ctx.Err(); err != nil {
							return err
						}
						if entry.LogicalPath != pageLogical && !strings.HasPrefix(entry.LogicalPath, "packages/") {
							return fmt.Errorf("invalid cached PyPI path %s: %w", entry.LogicalPath, domain.ErrConflict)
						}
						_, info, err := repo.Resolve(entry.LogicalPath)
						if err != nil {
							return fmt.Errorf("PyPI project %s references unavailable %s: %w", project, entry.LogicalPath, err)
						}
						if info.Size() != entry.Size {
							return fmt.Errorf("PyPI cached file %s size changed; run integrity check before rebuilding: %w", entry.LogicalPath, domain.ErrConflict)
						}
						entry.UpdatedAt = info.ModTime()
						return put(entry)
					}); err != nil {
						f.Close()
						return stats, err
					}
					if err = f.Close(); err != nil {
						return stats, err
					}
					if err = completeProject(project, digest, pageInfo); err != nil {
						return stats, err
					}
					continue
				}
				if _, err = f.Seek(0, io.SeekStart); err != nil {
					f.Close()
					return stats, err
				}
			}
		}
		pageURL, err := url.Parse(strings.TrimRight(req.Source.PublicURL, "/") + "/simple/" + url.PathEscape(project) + "/index.html")
		if err != nil {
			f.Close()
			return stats, err
		}
		h := sha256.New()
		z := xhtml.NewTokenizer(io.TeeReader(f, h))
		for {
			if err = ctx.Err(); err != nil {
				f.Close()
				return stats, err
			}
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
		if err = completeProject(project, pageSHA, pageInfo); err != nil {
			return stats, err
		}
	}
	if resumable != nil {
		// A new directory during the scan invalidates the initial project snapshot.
		// Do not publish a catalog that silently omits a newly added project.
		current, readErr := os.ReadDir(simpleDir)
		if readErr != nil {
			return stats, readErr
		}
		var total int64
		for _, item := range current {
			if item.IsDir() {
				total++
			}
		}
		if total != starting.TotalProjects {
			return stats, fmt.Errorf("PyPI project list changed during inventory: %w", domain.ErrConflict)
		}
		// A changed directory must not be silently omitted after a restart.
		if len(projects) != len(current) {
			return stats, fmt.Errorf("PyPI project directory changed during inventory: %w", domain.ErrConflict)
		}
		for i := range projects {
			if projects[i].Name() != current[i].Name() || projects[i].IsDir() != current[i].IsDir() {
				return stats, fmt.Errorf("PyPI project directory changed during inventory: %w", domain.ErrConflict)
			}
		}
		progress, flushErr := resumable.Flush(ctx)
		if flushErr != nil {
			return stats, flushErr
		}
		if req.Report != nil {
			if err = req.Report(progress); err != nil {
				return stats, err
			}
		}
		// Catch changes to already checked pages after a restart, even when
		// they belong to projects before the saved cursor.
		if err = resumable.VerifyPages(ctx, func(project string, size, mtime int64) error {
			_, info, statErr := repo.Resolve(path.Join("simple", project, "index.html"))
			if statErr != nil || info.Size() != size || info.ModTime().UnixNano() != mtime {
				return fmt.Errorf("PyPI index %s changed after checkpoint; restart inventory: %w", project, domain.ErrConflict)
			}
			return nil
		}); err != nil {
			return stats, err
		}
		// A paused scan may outlive external filesystem changes. Recheck
		// all previously recorded package sizes and existence before activation.
		if starting.CompletedProjects > 0 && cache != nil {
			freshRepo, err := common.OpenLocalRepository(req.Source.RootPath)
			if err != nil {
				return stats, err
			}
			if err = cache.VerifyEntries(ctx, func(entry domain.CatalogEntry) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				_, info, err := freshRepo.Resolve(entry.LogicalPath)
				if err != nil {
					return fmt.Errorf("PyPI checkpoint file %s unavailable: %w", entry.LogicalPath, err)
				}
				if info.Size() != entry.Size {
					return fmt.Errorf("PyPI checkpoint file %s changed size: %w", entry.LogicalPath, domain.ErrConflict)
				}
				return nil
			}); err != nil {
				return stats, err
			}
		}
	} else {
		if err = report(true); err != nil {
			return stats, err
		}
	}
	stats, err = sink.Stats(ctx)
	return stats, err
}

var _ ports.InventoryAdapter = (*Adapter)(nil)

func inventoryPageDigest(ctx context.Context, r io.Reader) (string, error) {
	h := sha256.New()
	buf := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := r.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if err == io.EOF {
			return hex.EncodeToString(h.Sum(nil)), nil
		}
		if err != nil {
			return "", err
		}
	}
}
