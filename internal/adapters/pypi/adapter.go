package pypi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ynw0/airgap-mirror/internal/adapters/common"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

const Provider = "pypi"
const simpleJSON = "application/vnd.pypi.simple.v1+json"

type Adapter struct{ Client *http.Client }

func New(client *http.Client) *Adapter   { return &Adapter{Client: client} }
func (*Adapter) Type() domain.SourceType { return domain.SourcePyPI }
func (*Adapter) Provider() string        { return Provider }

func (a *Adapter) ValidateConfig(ctx context.Context, source domain.Source) error {
	_ = ctx
	if source.Type != domain.SourcePyPI || source.Provider != Provider {
		return fmt.Errorf("source must be pypi/%s: %w", Provider, domain.ErrInvalid)
	}
	for name, raw := range map[string]string{"upstreamUrl": source.UpstreamURL, "publicUrl": source.PublicURL} {
		u, err := url.ParseRequestURI(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("%s must be an absolute HTTP(S) URL: %w", name, domain.ErrInvalid)
		}
	}
	return nil
}

func simpleURL(base string, project string) string {
	base = strings.TrimRight(base, "/")
	if project == "" {
		return base + "/"
	}
	return base + "/" + url.PathEscape(project) + "/"
}

func parseSerialHeader(resp *http.Response) (int64, error) {
	raw := strings.TrimSpace(resp.Header.Get("X-PyPI-Last-Serial"))
	if raw == "" {
		return 0, fmt.Errorf("PyPI response missing X-PyPI-Last-Serial: %w", domain.ErrInvalid)
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid X-PyPI-Last-Serial %q: %w", raw, domain.ErrInvalid)
	}
	return n, nil
}

func (a *Adapter) Probe(ctx context.Context, source domain.Source) (domain.UpstreamState, error) {
	resp, err := common.Do(ctx, a.Client, http.MethodGet, simpleURL(source.UpstreamURL, ""), simpleJSON)
	if err != nil {
		return domain.UpstreamState{}, err
	}
	defer resp.Body.Close()
	serial, err := parseSerialHeader(resp)
	if err != nil {
		return domain.UpstreamState{}, err
	}
	return domain.UpstreamState{Cursor: domain.Cursor{Kind: "pypi-serial", Value: strconv.FormatInt(serial, 10)}, ObservedAt: time.Now()}, nil
}

func baseSerial(c domain.Cursor) (int64, error) {
	if c.Empty() {
		return 0, nil
	}
	if c.Kind != "pypi-serial" {
		return 0, fmt.Errorf("expected pypi-serial cursor, got %s: %w", c.Kind, domain.ErrInvalid)
	}
	n, err := strconv.ParseInt(c.Value, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid PyPI base serial %q: %w", c.Value, domain.ErrInvalid)
	}
	return n, nil
}

func normalizeProject(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	sep := false
	for _, r := range name {
		switch r {
		case '-', '_', '.':
			if b.Len() > 0 {
				sep = true
			}
		default:
			if sep {
				b.WriteByte('-')
				sep = false
			}
			b.WriteRune(r)
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

type rootMeta struct {
	APIVersion string `json:"api-version"`
	LastSerial int64  `json:"_last-serial"`
}

type rootProject struct {
	Name       string `json:"name"`
	LastSerial int64  `json:"_last-serial"`
}

func validateAPIVersion(v string) error {
	if v == "" {
		return nil
	}
	major, _, ok := strings.Cut(v, ".")
	if !ok {
		major = v
	}
	if major != "1" {
		return fmt.Errorf("unsupported PyPI Simple API version %s: %w", v, domain.ErrInvalid)
	}
	return nil
}

func streamRootChanges(ctx context.Context, r io.Reader, base int64, ns string, set ports.WorksetStore) (rootMeta, int64, error) {
	var meta rootMeta
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return meta, 0, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return meta, 0, fmt.Errorf("PyPI root JSON is not an object: %w", domain.ErrInvalid)
	}
	var changed int64
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return meta, changed, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return meta, changed, fmt.Errorf("invalid PyPI root JSON key: %w", domain.ErrInvalid)
		}
		switch key {
		case "meta":
			if err = dec.Decode(&meta); err != nil {
				return meta, changed, err
			}
		case "projects":
			tok, err = dec.Token()
			if err != nil {
				return meta, changed, err
			}
			if d, ok := tok.(json.Delim); !ok || d != '[' {
				return meta, changed, fmt.Errorf("PyPI projects is not an array: %w", domain.ErrInvalid)
			}
			for dec.More() {
				if err = ctx.Err(); err != nil {
					return meta, changed, err
				}
				var project rootProject
				if err = dec.Decode(&project); err != nil {
					return meta, changed, err
				}
				if project.Name == "" || project.LastSerial <= 0 {
					return meta, changed, fmt.Errorf("PyPI project missing name/_last-serial: %w", domain.ErrInvalid)
				}
				if project.LastSerial <= base {
					continue
				}
				normalized := normalizeProject(project.Name)
				if normalized == "" {
					return meta, changed, fmt.Errorf("invalid PyPI project name %q: %w", project.Name, domain.ErrInvalid)
				}
				fresh, addErr := set.Add(ctx, ns, normalized)
				if addErr != nil {
					return meta, changed, addErr
				}
				if fresh {
					changed++
				}
			}
			if _, err = dec.Token(); err != nil {
				return meta, changed, err
			}
		default:
			var discard json.RawMessage
			if err = dec.Decode(&discard); err != nil {
				return meta, changed, err
			}
		}
	}
	if _, err = dec.Token(); err != nil {
		return meta, changed, err
	}
	return meta, changed, validateAPIVersion(meta.APIVersion)
}

type projectMeta struct {
	APIVersion string `json:"api-version"`
}

type projectFile struct {
	Filename       string            `json:"filename"`
	URL            string            `json:"url"`
	Hashes         map[string]string `json:"hashes"`
	RequiresPython *string           `json:"requires-python"`
	Yanked         json.RawMessage   `json:"yanked"`
	Size           int64             `json:"size"`
}

type projectResponse struct {
	Meta  projectMeta   `json:"meta"`
	Name  string        `json:"name"`
	Files []projectFile `json:"files"`
}

type fileView struct {
	File          projectFile
	LogicalPath   string
	EscapedPath   string
	SHA256        string
	YankedPresent bool
	YankedReason  string
}

func parseYanked(raw json.RawMessage) (bool, string, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "false" {
		return false, "", nil
	}
	if string(raw) == "true" {
		return true, "", nil
	}
	var reason string
	if err := json.Unmarshal(raw, &reason); err != nil {
		return false, "", fmt.Errorf("invalid yanked value: %w", domain.ErrInvalid)
	}
	return true, reason, nil
}

func fileViewFrom(f projectFile) (fileView, error) {
	var out fileView
	out.File = f
	if f.Filename == "" || f.URL == "" || f.Size < 0 {
		return out, fmt.Errorf("PyPI file missing filename/url/size: %w", domain.ErrInvalid)
	}
	sum := strings.ToLower(f.Hashes["sha256"])
	if len(sum) != 64 {
		return out, fmt.Errorf("PyPI file %s missing sha256: %w", f.Filename, domain.ErrInvalid)
	}
	for _, c := range sum {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return out, fmt.Errorf("PyPI file %s has invalid sha256: %w", f.Filename, domain.ErrInvalid)
		}
	}
	u, err := url.Parse(f.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return out, fmt.Errorf("PyPI file %s has invalid URL: %w", f.Filename, domain.ErrInvalid)
	}
	clean := path.Clean(u.Path)
	if clean == "." || !strings.HasPrefix(clean, "/packages/") {
		return out, fmt.Errorf("PyPI file %s is outside /packages/: %w", f.Filename, domain.ErrInvalid)
	}
	if path.Base(clean) != f.Filename {
		return out, fmt.Errorf("PyPI file URL basename %q does not match filename %q: %w", path.Base(clean), f.Filename, domain.ErrInvalid)
	}
	u.Fragment = ""
	u.RawFragment = ""
	out.File.URL = u.String()
	out.LogicalPath = strings.TrimPrefix(clean, "/")
	out.EscapedPath = strings.TrimPrefix(u.EscapedPath(), "/")
	out.SHA256 = sum
	out.YankedPresent, out.YankedReason, err = parseYanked(f.Yanked)
	return out, err
}

func renderProjectPage(name string, files []fileView) []byte {
	sort.Slice(files, func(i, j int) bool {
		if files[i].File.Filename != files[j].File.Filename {
			return files[i].File.Filename < files[j].File.Filename
		}
		return files[i].EscapedPath < files[j].EscapedPath
	})
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html><head><meta name=\"pypi:repository-version\" content=\"1.0\"><title>Links for ")
	b.WriteString(html.EscapeString(name))
	b.WriteString("</title></head><body>\n<h1>Links for ")
	b.WriteString(html.EscapeString(name))
	b.WriteString("</h1>\n")
	for _, f := range files {
		b.WriteString("<a href=\"../../")
		b.WriteString(html.EscapeString(f.EscapedPath))
		b.WriteString("#sha256=")
		b.WriteString(f.SHA256)
		b.WriteString("\"")
		if f.File.RequiresPython != nil {
			b.WriteString(" data-requires-python=\"")
			b.WriteString(html.EscapeString(*f.File.RequiresPython))
			b.WriteString("\"")
		}
		if f.YankedPresent {
			b.WriteString(" data-yanked=\"")
			b.WriteString(html.EscapeString(f.YankedReason))
			b.WriteString("\"")
		}
		b.WriteString(">")
		b.WriteString(html.EscapeString(f.File.Filename))
		b.WriteString("</a><br />\n")
	}
	b.WriteString("</body></html>\n")
	return []byte(b.String())
}

func classify(ctx context.Context, catalog ports.CatalogStore, sourceID string, a domain.Artifact) (domain.ArtifactOperation, bool, error) {
	existing, err := catalog.Get(ctx, sourceID, a.LogicalPath)
	if err == nil {
		if existing.Size == a.Size && strings.EqualFold(existing.SHA256, a.SHA256) {
			return domain.ArtifactAdd, false, nil
		}
		if !a.Metadata {
			return "", false, fmt.Errorf("PyPI immutable path changed %s: %w", a.LogicalPath, domain.ErrConflict)
		}
		return domain.ArtifactUpdate, true, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return "", false, err
	}
	return domain.ArtifactAdd, true, nil
}

func (a *Adapter) Analyze(ctx context.Context, req ports.AnalyzeRequest, sink ports.PlanSink) (plan domain.EpochPlan, err error) {
	if req.Workset == nil || req.Catalog == nil || req.Generated == nil {
		return plan, fmt.Errorf("PyPI analyze requires workset, catalog, and generated store: %w", domain.ErrInvalid)
	}
	base, err := baseSerial(req.Base)
	if err != nil {
		return plan, err
	}
	epochID, err := domain.NewID()
	if err != nil {
		return plan, err
	}
	ns := "pypi:" + epochID + ":projects"
	if err = req.Workset.Reset(ctx, ns); err != nil {
		return plan, err
	}
	resp, err := common.Do(ctx, a.Client, http.MethodGet, simpleURL(req.Source.UpstreamURL, ""), simpleJSON)
	if err != nil {
		return plan, err
	}
	target, err := parseSerialHeader(resp)
	if err != nil {
		resp.Body.Close()
		return plan, err
	}
	if target < base {
		resp.Body.Close()
		return plan, fmt.Errorf("PyPI serial moved backwards %d -> %d: %w", base, target, domain.ErrConflict)
	}
	meta, changed, err := streamRootChanges(ctx, resp.Body, base, ns, req.Workset)
	resp.Body.Close()
	if err != nil {
		return plan, err
	}
	if meta.LastSerial != 0 && meta.LastSerial != target {
		return plan, fmt.Errorf("PyPI root serial header/body mismatch %d/%d: %w", target, meta.LastSerial, domain.ErrConflict)
	}
	epoch := domain.Epoch{ID: epochID, SourceID: req.Source.ID, BaseCursor: req.Base, TargetCursor: domain.Cursor{Kind: "pypi-serial", Value: strconv.FormatInt(target, 10)}, Status: domain.EpochPlanned, PublishUnitCount: changed, CreatedAt: time.Now()}
	plan.Epoch = epoch
	plan.PublishUnitCount = changed
	if err = sink.Begin(ctx, epoch, req.CapsuleID); err != nil {
		return plan, err
	}
	committed := false
	defer func() {
		if err != nil && !committed {
			_ = sink.Abort(context.Background(), epochID, err)
		}
	}()
	var totalBytes, totalObjects, metadataCount int64
	err = req.Workset.Walk(ctx, ns, func(project string) error {
		resp, e := common.Do(ctx, a.Client, http.MethodGet, simpleURL(req.Source.UpstreamURL, project), simpleJSON)
		if e != nil {
			return e
		}
		body, _, e := common.ReadAllSHA256(resp.Body, 128<<20)
		resp.Body.Close()
		if e != nil {
			return e
		}
		var pr projectResponse
		if e = json.Unmarshal(body, &pr); e != nil {
			return fmt.Errorf("decode PyPI project %s: %w", project, e)
		}
		if e = validateAPIVersion(pr.Meta.APIVersion); e != nil {
			return e
		}
		if pr.Name != "" && normalizeProject(pr.Name) != project {
			return fmt.Errorf("PyPI project response name %q does not match %q: %w", pr.Name, project, domain.ErrConflict)
		}
		unitID, e := domain.NewID()
		if e != nil {
			return e
		}
		metadataPath := path.Join("simple", project, "index.html")
		unit := domain.PublishUnit{ID: unitID, EpochID: epochID, SourceID: req.Source.ID, Key: "pypi:" + project, Status: domain.PublishWaiting, MetadataPath: metadataPath}
		if e = sink.PutPublishUnit(ctx, unit); e != nil {
			return e
		}
		views := make([]fileView, 0, len(pr.Files))
		var required int64
		for _, file := range pr.Files {
			view, x := fileViewFrom(file)
			if x != nil {
				return x
			}
			views = append(views, view)
			art := domain.Artifact{LogicalPath: view.LogicalPath, Size: file.Size, SHA256: view.SHA256, UpstreamURL: view.File.URL, PublishUnitID: unitID, PackageKey: project}
			op, needed, x := classify(ctx, req.Catalog, req.Source.ID, art)
			if x != nil {
				return x
			}
			if !needed {
				continue
			}
			art.ID, x = domain.NewID()
			if x != nil {
				return x
			}
			art.EpochID = epochID
			art.SourceID = req.Source.ID
			art.Operation = op
			attrs, marshalErr := json.Marshal(map[string]any{"requiresPython": file.RequiresPython, "yanked": json.RawMessage(file.Yanked)})
			if marshalErr != nil {
				return fmt.Errorf("encode PyPI artifact attributes for %s: %w", file.Filename, marshalErr)
			}
			art.Attributes = attrs
			if x = sink.PutArtifact(ctx, art); x != nil {
				return x
			}
			required++
			totalObjects++
			totalBytes += art.Size
			plan.ArtifactCount++
		}
		page := renderProjectPage(project, views)
		local, sha, size, x := req.Generated.Put(ctx, epochID, metadataPath, page)
		if x != nil {
			return x
		}
		metaArt := domain.Artifact{LogicalPath: metadataPath, Size: size, SHA256: sha, LocalSourcePath: local, PublishUnitID: unitID, PackageKey: project, Metadata: true}
		op, needed, x := classify(ctx, req.Catalog, req.Source.ID, metaArt)
		if x != nil {
			return x
		}
		if needed {
			metaArt.ID, x = domain.NewID()
			if x != nil {
				return x
			}
			metaArt.EpochID = epochID
			metaArt.SourceID = req.Source.ID
			metaArt.Operation = op
			if x = sink.PutArtifact(ctx, metaArt); x != nil {
				return x
			}
			required++
			totalObjects++
			totalBytes += metaArt.Size
			metadataCount++
			plan.ArtifactCount++
		}
		unit.Required = required
		return sink.PutPublishUnit(ctx, unit)
	})
	if err != nil {
		return plan, err
	}
	epoch.TotalBytes = totalBytes
	epoch.TotalObjects = totalObjects
	plan.Epoch = epoch
	plan.MetadataCount = metadataCount
	if err = sink.Commit(ctx, plan); err != nil {
		return plan, err
	}
	committed = true
	return plan, nil
}

func (*Adapter) MaterializeMetadata(context.Context, ports.MetadataRequest) ([]domain.GeneratedFile, error) {
	return nil, nil
}

func (*Adapter) ValidatePublish(_ context.Context, req ports.PublishValidationRequest) error {
	if req.Unit.Imported != req.Unit.Required {
		return fmt.Errorf("PyPI project publish unit incomplete %d/%d: %w", req.Unit.Imported, req.Unit.Required, domain.ErrConflict)
	}
	return nil
}

var _ ports.SourceAdapter = (*Adapter)(nil)
