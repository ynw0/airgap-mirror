package npm

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/ynw0/airgap-mirror/internal/pack"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

const Provider = "npmjs"

const (
	defaultReplicationURL = "https://replicate.npmjs.com/registry"
	defaultPageSize       = 10000
	packumentAccept       = "application/json"
	emptySHA256           = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

type Config struct {
	ReplicationURL  string `json:"replicationUrl"`
	ChangePageSize  int    `json:"changePageSize"`
	AllDocsPageSize int    `json:"allDocsPageSize"`
}

type Adapter struct{ Client *http.Client }

func New(client *http.Client) *Adapter   { return &Adapter{Client: client} }
func (*Adapter) Type() domain.SourceType { return domain.SourceNPM }
func (*Adapter) Provider() string        { return Provider }

func parseConfig(source domain.Source) (Config, error) {
	cfg := Config{ReplicationURL: defaultReplicationURL, ChangePageSize: defaultPageSize, AllDocsPageSize: defaultPageSize}
	if len(source.ConfigJSON) != 0 {
		if err := json.Unmarshal(source.ConfigJSON, &cfg); err != nil {
			return cfg, fmt.Errorf("decode npm config: %w", err)
		}
	}
	cfg.ReplicationURL = strings.TrimRight(strings.TrimSpace(cfg.ReplicationURL), "/")
	if cfg.ReplicationURL == "" {
		cfg.ReplicationURL = defaultReplicationURL
	}
	if cfg.ChangePageSize <= 0 || cfg.ChangePageSize > 10000 {
		return cfg, fmt.Errorf("changePageSize must be 1..10000: %w", domain.ErrInvalid)
	}
	if cfg.AllDocsPageSize <= 0 || cfg.AllDocsPageSize > 10000 {
		return cfg, fmt.Errorf("allDocsPageSize must be 1..10000: %w", domain.ErrInvalid)
	}
	return cfg, nil
}

func validateHTTPURL(name, raw string) error {
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL: %w", name, domain.ErrInvalid)
	}
	return nil
}

func (a *Adapter) ValidateConfig(ctx context.Context, source domain.Source) error {
	_ = ctx
	if source.Type != domain.SourceNPM || source.Provider != Provider {
		return fmt.Errorf("source must be npm/%s: %w", Provider, domain.ErrInvalid)
	}
	if err := validateHTTPURL("upstreamUrl", source.UpstreamURL); err != nil {
		return err
	}
	if err := validateHTTPURL("publicUrl", source.PublicURL); err != nil {
		return err
	}
	cfg, err := parseConfig(source)
	if err != nil {
		return err
	}
	return validateHTTPURL("replicationUrl", cfg.ReplicationURL)
}

type seqNumber int64

func (s *seqNumber) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		return fmt.Errorf("empty sequence")
	}
	if strings.HasPrefix(raw, `"`) {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		raw = text
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return fmt.Errorf("invalid sequence %q", raw)
	}
	*s = seqNumber(n)
	return nil
}

func (a *Adapter) replicationRequest(ctx context.Context, method, rawURL string) (*http.Response, error) {
	req, err := common.NewRequest(ctx, method, rawURL)
	if err != nil {
		return nil, err
	}
	// 该头在 2025 迁移期后会被忽略，保留它不会改变新 API 语义。
	req.Header.Set("npm-replication-opt-in", "true")
	resp, err := common.Client(a.Client).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return nil, fmt.Errorf("%s %s: http %d: %s", method, rawURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

type rootResponse struct {
	DBName    string    `json:"db_name"`
	Engine    string    `json:"engine"`
	DocCount  int64     `json:"doc_count"`
	UpdateSeq seqNumber `json:"update_seq"`
}

func (a *Adapter) root(ctx context.Context, cfg Config) (rootResponse, error) {
	var out rootResponse
	resp, err := a.replicationRequest(ctx, http.MethodGet, cfg.ReplicationURL+"/")
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	if err = dec.Decode(&out); err != nil {
		return out, fmt.Errorf("decode npm replication root: %w", err)
	}
	if out.DBName != "registry" || out.UpdateSeq < 0 {
		return out, fmt.Errorf("unexpected npm replication root: %w", domain.ErrInvalid)
	}
	return out, nil
}

func (a *Adapter) Probe(ctx context.Context, source domain.Source) (domain.UpstreamState, error) {
	cfg, err := parseConfig(source)
	if err != nil {
		return domain.UpstreamState{}, err
	}
	root, err := a.root(ctx, cfg)
	if err != nil {
		return domain.UpstreamState{}, err
	}
	return domain.UpstreamState{
		Cursor:     domain.Cursor{Kind: "npm-update-seq", Value: strconv.FormatInt(int64(root.UpdateSeq), 10)},
		ObservedAt: time.Now(),
		Summary:    fmt.Sprintf("documents=%d", root.DocCount),
	}, nil
}

func parseBaseCursor(c domain.Cursor) (int64, error) {
	if c.Empty() {
		return 0, nil
	}
	if c.Kind != "npm-update-seq" {
		return 0, fmt.Errorf("expected npm-update-seq cursor, got %s: %w", c.Kind, domain.ErrInvalid)
	}
	n, err := strconv.ParseInt(c.Value, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid npm cursor %q: %w", c.Value, domain.ErrInvalid)
	}
	return n, nil
}

type workState struct {
	Rev     string `json:"rev,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
}

func putWork(ctx context.Context, set ports.WorksetStore, ns, name, rev string, deleted bool) error {
	if name == "" || strings.HasPrefix(name, "_") {
		return nil
	}
	body, err := json.Marshal(workState{Rev: rev, Deleted: deleted})
	if err != nil {
		return err
	}
	return set.Put(ctx, ns, name, string(body))
}

func decodeWork(value string) (workState, error) {
	var out workState
	if err := json.Unmarshal([]byte(value), &out); err != nil {
		return out, err
	}
	if !out.Deleted && out.Rev == "" {
		return out, fmt.Errorf("npm workset revision is empty: %w", domain.ErrInvalid)
	}
	return out, nil
}

type allDocsRow struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Value struct {
		Rev string `json:"rev"`
	} `json:"value"`
}

type allDocsPage struct {
	TotalRows int64        `json:"total_rows"`
	Rows      []allDocsRow `json:"rows"`
}

func (a *Adapter) enumerateAllDocs(ctx context.Context, cfg Config, ns string, set ports.WorksetStore) error {
	var start string
	for {
		u, err := url.Parse(cfg.ReplicationURL + "/_all_docs")
		if err != nil {
			return err
		}
		q := u.Query()
		q.Set("limit", strconv.Itoa(cfg.AllDocsPageSize))
		if start != "" {
			encodedStart, err := json.Marshal(start)
			if err != nil {
				return err
			}
			q.Set("startkey", string(encodedStart))
			q.Set("startkey_docid", start)
		}
		u.RawQuery = q.Encode()
		resp, err := a.replicationRequest(ctx, http.MethodGet, u.String())
		if err != nil {
			return err
		}
		var page allDocsPage
		dec := json.NewDecoder(io.LimitReader(resp.Body, 32<<20))
		err = dec.Decode(&page)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("decode npm _all_docs: %w", err)
		}
		if len(page.Rows) == 0 {
			return nil
		}
		newRows := 0
		last := start
		for _, row := range page.Rows {
			if err = ctx.Err(); err != nil {
				return err
			}
			if row.ID == "" || row.ID != row.Key || row.Value.Rev == "" {
				return fmt.Errorf("invalid npm _all_docs row: %w", domain.ErrInvalid)
			}
			if start != "" && row.ID == start {
				continue
			}
			if err = putWork(ctx, set, ns, row.ID, row.Value.Rev, false); err != nil {
				return err
			}
			newRows++
			last = row.ID
		}
		if newRows == 0 {
			return nil
		}
		if len(page.Rows) < cfg.AllDocsPageSize {
			return nil
		}
		if last == "" || last == start {
			return fmt.Errorf("npm _all_docs pagination made no progress: %w", domain.ErrConflict)
		}
		start = last
	}
}

type changeRow struct {
	Seq     seqNumber `json:"seq"`
	ID      string    `json:"id"`
	Deleted bool      `json:"deleted"`
	Changes []struct {
		Rev string `json:"rev"`
	} `json:"changes"`
}

type changesPage struct {
	Results []changeRow `json:"results"`
	LastSeq seqNumber   `json:"last_seq"`
}

func (a *Adapter) applyChanges(ctx context.Context, cfg Config, ns string, set ports.WorksetStore, since, target int64) error {
	if since >= target {
		return nil
	}
	for since < target {
		u, err := url.Parse(cfg.ReplicationURL + "/_changes")
		if err != nil {
			return err
		}
		q := u.Query()
		q.Set("since", strconv.FormatInt(since, 10))
		q.Set("limit", strconv.Itoa(cfg.ChangePageSize))
		u.RawQuery = q.Encode()
		resp, err := a.replicationRequest(ctx, http.MethodGet, u.String())
		if err != nil {
			return err
		}
		var page changesPage
		dec := json.NewDecoder(io.LimitReader(resp.Body, 64<<20))
		err = dec.Decode(&page)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("decode npm _changes: %w", err)
		}
		if int64(page.LastSeq) <= since && len(page.Results) == 0 {
			return fmt.Errorf("npm _changes made no progress since %d: %w", since, domain.ErrConflict)
		}
		for _, row := range page.Results {
			seq := int64(row.Seq)
			if seq <= since || seq > target {
				continue
			}
			rev := ""
			if len(row.Changes) > 0 {
				rev = row.Changes[len(row.Changes)-1].Rev
			}
			if !row.Deleted && rev == "" {
				return fmt.Errorf("npm change %s seq=%d missing revision: %w", row.ID, seq, domain.ErrInvalid)
			}
			if err = putWork(ctx, set, ns, row.ID, rev, row.Deleted); err != nil {
				return err
			}
		}
		next := int64(page.LastSeq)
		if next <= since {
			return fmt.Errorf("npm _changes last_seq=%d did not advance from %d: %w", next, since, domain.ErrConflict)
		}
		if next >= target {
			return nil
		}
		since = next
	}
	return nil
}

func revGeneration(rev string) (int64, error) {
	prefix, _, ok := strings.Cut(rev, "-")
	if !ok || prefix == "" {
		return 0, fmt.Errorf("invalid npm revision %q: %w", rev, domain.ErrInvalid)
	}
	n, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid npm revision %q: %w", rev, domain.ErrInvalid)
	}
	return n, nil
}

func compareRevision(actual, expected string) (int, error) {
	if actual == expected {
		return 0, nil
	}
	a, err := revGeneration(actual)
	if err != nil {
		return 0, err
	}
	e, err := revGeneration(expected)
	if err != nil {
		return 0, err
	}
	if a < e {
		return -1, nil
	}
	if a > e {
		return 1, nil
	}
	return 0, fmt.Errorf("npm revision generation collision actual=%s expected=%s: %w", actual, expected, domain.ErrConflict)
}

func packageURL(base, name string) string {
	return strings.TrimRight(base, "/") + "/" + url.PathEscape(name)
}

func packumentLogicalPath(name string) string {
	return "packuments/" + url.PathEscape(name) + ".json"
}

func publicArtifactURL(base, logical string) (string, error) {
	if err := pack.ValidateLogicalPath(logical); err != nil {
		return "", err
	}
	parts := strings.Split(logical, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.TrimRight(base, "/") + "/" + strings.Join(parts, "/"), nil
}

func decodePackument(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, fmt.Errorf("npm packument is not an object: %w", domain.ErrInvalid)
	}
	return doc, nil
}

func stringField(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func (a *Adapter) fetchPackument(ctx context.Context, source domain.Source, name string) (map[string]any, int, error) {
	req, err := common.NewRequest(ctx, http.MethodGet, packageURL(source.UpstreamURL, name))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", packumentAccept)
	resp, err := common.Client(a.Client).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, resp.StatusCode, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return nil, resp.StatusCode, fmt.Errorf("GET npm packument %s: http %d: %s", name, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	const maxPackumentBytes = 256 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPackumentBytes+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(body) > maxPackumentBytes {
		return nil, resp.StatusCode, fmt.Errorf("npm packument %s exceeds 256 MiB: %w", name, domain.ErrInvalid)
	}
	doc, err := decodePackument(body)
	return doc, resp.StatusCode, err
}

func normalizeIntegrity(dist map[string]any) (string, error) {
	if raw, ok := stringField(dist, "integrity"); ok && strings.TrimSpace(raw) != "" {
		bestRank := -1
		best := ""
		rank := map[string]int{"sha1": 1, "sha256": 2, "sha384": 3, "sha512": 4}
		for _, token := range strings.Fields(raw) {
			alg, value, ok := strings.Cut(token, "-")
			if !ok {
				continue
			}
			alg = strings.ToLower(alg)
			r, supported := rank[alg]
			if !supported {
				continue
			}
			if _, err := base64.StdEncoding.DecodeString(value); err != nil {
				return "", fmt.Errorf("invalid npm dist.integrity %q: %w", token, domain.ErrInvalid)
			}
			if r > bestRank {
				bestRank = r
				best = alg + "-" + value
			}
		}
		if best == "" {
			return "", fmt.Errorf("npm dist.integrity has no supported digest: %w", domain.ErrInvalid)
		}
		return best, nil
	}
	shasum, ok := stringField(dist, "shasum")
	if !ok || len(shasum) != 40 {
		return "", fmt.Errorf("npm dist lacks integrity and valid shasum: %w", domain.ErrInvalid)
	}
	bytes, err := hex.DecodeString(strings.ToLower(shasum))
	if err != nil || len(bytes) != sha1.Size {
		return "", fmt.Errorf("invalid npm dist.shasum %q: %w", shasum, domain.ErrInvalid)
	}
	return "sha1-" + base64.StdEncoding.EncodeToString(bytes), nil
}

type artifactAttrs struct {
	Package           string `json:"package"`
	Version           string `json:"version,omitempty"`
	UpstreamRev       string `json:"upstreamRev"`
	UpstreamIntegrity string `json:"upstreamIntegrity,omitempty"`
}

func attrsJSON(v artifactAttrs) (json.RawMessage, error) {
	body, err := json.Marshal(v)
	return json.RawMessage(body), err
}

func catalogIntegrity(entry domain.CatalogEntry) (string, string) {
	var attrs artifactAttrs
	if len(entry.Attributes) == 0 || json.Unmarshal(entry.Attributes, &attrs) != nil {
		return "", ""
	}
	return attrs.UpstreamIntegrity, attrs.UpstreamRev
}

func tarballLogicalPath(rawURL string, registryBase string) (string, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", fmt.Errorf("invalid npm tarball URL %q: %w", rawURL, domain.ErrInvalid)
	}
	registry, err := url.Parse(registryBase)
	if err != nil {
		return "", "", err
	}
	if !strings.EqualFold(u.Host, registry.Host) {
		return "", "", fmt.Errorf("npm tarball host %s differs from registry host %s: %w", u.Host, registry.Host, domain.ErrInvalid)
	}
	clean := path.Clean(u.Path)
	if clean == "." || clean == "/" || !strings.HasSuffix(strings.ToLower(clean), ".tgz") {
		return "", "", fmt.Errorf("invalid npm tarball path %q: %w", u.Path, domain.ErrInvalid)
	}
	logical := strings.TrimPrefix(clean, "/")
	if err = pack.ValidateLogicalPath(logical); err != nil {
		return "", "", err
	}
	u.Fragment = ""
	u.RawFragment = ""
	return logical, u.String(), nil
}

func (a *Adapter) headTarball(ctx context.Context, rawURL string) (int64, error) {
	resp, err := common.Do(ctx, a.Client, http.MethodHead, rawURL, "")
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	if resp.ContentLength < 0 {
		return 0, fmt.Errorf("npm tarball HEAD missing Content-Length for %s: %w", rawURL, domain.ErrInvalid)
	}
	return resp.ContentLength, nil
}

func (a *Adapter) planDeletedPackage(ctx context.Context, req ports.AnalyzeRequest, sink ports.PlanSink, epoch domain.Epoch, name string, unitID string) (int64, int64, error) {
	logical := packumentLogicalPath(name)
	_, err := req.Catalog.Get(ctx, req.Source.ID, logical)
	if errors.Is(err, domain.ErrNotFound) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	attrs, err := attrsJSON(artifactAttrs{Package: name})
	if err != nil {
		return 0, 0, err
	}
	aid, err := domain.NewID()
	if err != nil {
		return 0, 0, err
	}
	artifact := domain.Artifact{ID: aid, EpochID: epoch.ID, SourceID: req.Source.ID, LogicalPath: logical, Size: 0, SHA256: emptySHA256, Operation: domain.ArtifactDelete, PublishUnitID: unitID, PackageKey: name, Metadata: true, Attributes: attrs}
	if err = sink.PutArtifact(ctx, artifact); err != nil {
		return 0, 0, err
	}
	return 1, 1, nil
}

func (a *Adapter) planPackage(ctx context.Context, req ports.AnalyzeRequest, sink ports.PlanSink, epoch domain.Epoch, name string, work workState) (int64, int64, int64, int64, error) {
	unitID, err := domain.NewID()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	doc, status, err := a.fetchPackument(ctx, req.Source, name)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	if status == http.StatusNotFound {
		if !work.Deleted {
			return 0, 0, 0, 0, fmt.Errorf("npm registry packument %s is not visible yet for replication revision %s; retry analysis later: %w", name, work.Rev, domain.ErrConflict)
		}
		objects, metadata, err := a.planDeletedPackage(ctx, req, sink, epoch, name, unitID)
		if err != nil || objects == 0 {
			return 0, objects, metadata, 0, err
		}
		u := domain.PublishUnit{ID: unitID, EpochID: epoch.ID, SourceID: req.Source.ID, Key: name, Status: domain.PublishWaiting, Required: objects, MetadataPath: packumentLogicalPath(name)}
		if err = sink.PutPublishUnit(ctx, u); err != nil {
			return 0, 0, 0, 0, err
		}
		return 1, objects, metadata, 0, nil
	}

	actualName, ok := stringField(doc, "name")
	if !ok || actualName != name {
		return 0, 0, 0, 0, fmt.Errorf("npm packument name %q does not match change id %q: %w", actualName, name, domain.ErrConflict)
	}
	actualRev, ok := stringField(doc, "_rev")
	if !ok || actualRev == "" {
		return 0, 0, 0, 0, fmt.Errorf("npm packument %s missing _rev: %w", name, domain.ErrInvalid)
	}
	cmp, err := compareRevision(actualRev, work.Rev)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	if cmp < 0 {
		return 0, 0, 0, 0, fmt.Errorf("npm registry packument %s revision %s is behind replication revision %s; retry analysis later: %w", name, actualRev, work.Rev, domain.ErrConflict)
	}
	if work.Deleted && work.Rev != "" && cmp == 0 {
		return 0, 0, 0, 0, fmt.Errorf("npm package %s is visible at deletion revision %s; retry analysis later: %w", name, work.Rev, domain.ErrConflict)
	}

	versionsRaw, ok := doc["versions"]
	if !ok {
		versionsRaw = map[string]any{}
	}
	versions, ok := versionsRaw.(map[string]any)
	if !ok {
		return 0, 0, 0, 0, fmt.Errorf("npm packument %s versions is not an object: %w", name, domain.ErrInvalid)
	}

	var planned []domain.Artifact
	seen := map[string]string{}
	var totalBytes int64
	versionKeys := make([]string, 0, len(versions))
	for version := range versions {
		versionKeys = append(versionKeys, version)
	}
	sort.Strings(versionKeys)
	for _, version := range versionKeys {
		value, ok := versions[version].(map[string]any)
		if !ok {
			return 0, 0, 0, 0, fmt.Errorf("npm %s@%s metadata is not an object: %w", name, version, domain.ErrInvalid)
		}
		if field, exists := stringField(value, "version"); exists && field != version {
			return 0, 0, 0, 0, fmt.Errorf("npm %s version key %s != version field %s: %w", name, version, field, domain.ErrConflict)
		}
		distValue, exists := value["dist"]
		if !exists {
			continue
		}
		dist, ok := distValue.(map[string]any)
		if !ok {
			return 0, 0, 0, 0, fmt.Errorf("npm %s@%s dist is not an object: %w", name, version, domain.ErrInvalid)
		}
		tarball, ok := stringField(dist, "tarball")
		if !ok || tarball == "" {
			return 0, 0, 0, 0, fmt.Errorf("npm %s@%s dist.tarball missing: %w", name, version, domain.ErrInvalid)
		}
		integrity, err := normalizeIntegrity(dist)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("npm %s@%s: %w", name, version, err)
		}
		logical, cleanURL, err := tarballLogicalPath(tarball, req.Source.UpstreamURL)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("npm %s@%s: %w", name, version, err)
		}
		if prev, exists := seen[logical]; exists {
			if prev != integrity {
				return 0, 0, 0, 0, fmt.Errorf("npm packument %s reuses tarball path %s with different integrity: %w", name, logical, domain.ErrConflict)
			}
			publicURL, err := publicArtifactURL(req.Source.PublicURL, logical)
			if err != nil {
				return 0, 0, 0, 0, err
			}
			dist["tarball"] = publicURL
			continue
		}
		seen[logical] = integrity
		publicURL, err := publicArtifactURL(req.Source.PublicURL, logical)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		dist["tarball"] = publicURL

		existing, getErr := req.Catalog.Get(ctx, req.Source.ID, logical)
		if getErr == nil {
			oldIntegrity, _ := catalogIntegrity(existing)
			if oldIntegrity == "" {
				return 0, 0, 0, 0, fmt.Errorf("existing npm tarball %s lacks upstream integrity in catalog; run npm inventory before incremental sync: %w", logical, domain.ErrConflict)
			}
			if oldIntegrity != integrity {
				return 0, 0, 0, 0, fmt.Errorf("npm immutable tarball %s integrity changed: %w", logical, domain.ErrConflict)
			}
			continue
		}
		if !errors.Is(getErr, domain.ErrNotFound) {
			return 0, 0, 0, 0, getErr
		}
		size, err := a.headTarball(ctx, cleanURL)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("npm %s@%s tarball preflight: %w", name, version, err)
		}
		attrs, err := attrsJSON(artifactAttrs{Package: name, Version: version, UpstreamRev: actualRev, UpstreamIntegrity: integrity})
		if err != nil {
			return 0, 0, 0, 0, err
		}
		aid, err := domain.NewID()
		if err != nil {
			return 0, 0, 0, 0, err
		}
		planned = append(planned, domain.Artifact{ID: aid, EpochID: epoch.ID, SourceID: req.Source.ID, LogicalPath: logical, Size: size, UpstreamURL: cleanURL, UpstreamIntegrity: integrity, Operation: domain.ArtifactAdd, PublishUnitID: unitID, PackageKey: name, Version: version, Attributes: attrs})
		totalBytes += size
	}

	body, err := json.Marshal(doc)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("encode rewritten npm packument %s: %w", name, err)
	}
	logicalMeta := packumentLogicalPath(name)
	localPath, metaSHA, metaSize, err := req.Generated.Put(ctx, epoch.ID, logicalMeta, body)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	metadataChanged := true
	if existing, getErr := req.Catalog.Get(ctx, req.Source.ID, logicalMeta); getErr == nil {
		if existing.SHA256 == metaSHA && existing.Size == metaSize {
			metadataChanged = false
		}
	} else if !errors.Is(getErr, domain.ErrNotFound) {
		return 0, 0, 0, 0, getErr
	}
	if metadataChanged {
		attrs, err := attrsJSON(artifactAttrs{Package: name, UpstreamRev: actualRev})
		if err != nil {
			return 0, 0, 0, 0, err
		}
		aid, err := domain.NewID()
		if err != nil {
			return 0, 0, 0, 0, err
		}
		op := domain.ArtifactAdd
		if _, getErr := req.Catalog.Get(ctx, req.Source.ID, logicalMeta); getErr == nil {
			op = domain.ArtifactUpdate
		} else if !errors.Is(getErr, domain.ErrNotFound) {
			return 0, 0, 0, 0, getErr
		}
		planned = append(planned, domain.Artifact{ID: aid, EpochID: epoch.ID, SourceID: req.Source.ID, LogicalPath: logicalMeta, Size: metaSize, SHA256: metaSHA, LocalSourcePath: localPath, Operation: op, PublishUnitID: unitID, PackageKey: name, Metadata: true, Attributes: attrs})
		totalBytes += metaSize
	}
	if len(planned) == 0 {
		return 0, 0, 0, 0, nil
	}
	u := domain.PublishUnit{ID: unitID, EpochID: epoch.ID, SourceID: req.Source.ID, Key: name, Status: domain.PublishWaiting, Required: int64(len(planned)), MetadataPath: logicalMeta}
	if err = sink.PutPublishUnit(ctx, u); err != nil {
		return 0, 0, 0, 0, err
	}
	var metadataCount int64
	for _, artifact := range planned {
		if err = sink.PutArtifact(ctx, artifact); err != nil {
			return 0, 0, 0, 0, err
		}
		if artifact.Metadata {
			metadataCount++
		}
	}
	return 1, int64(len(planned)), metadataCount, totalBytes, nil
}

func (a *Adapter) Analyze(ctx context.Context, req ports.AnalyzeRequest, sink ports.PlanSink) (plan domain.EpochPlan, retErr error) {
	if sink == nil || req.Workset == nil || req.Catalog == nil || req.Generated == nil {
		return plan, fmt.Errorf("npm analysis requires sink, workset, catalog and generated store: %w", domain.ErrInvalid)
	}
	if err := a.ValidateConfig(ctx, req.Source); err != nil {
		return plan, err
	}
	cfg, err := parseConfig(req.Source)
	if err != nil {
		return plan, err
	}
	base, err := parseBaseCursor(req.Base)
	if err != nil {
		return plan, err
	}
	epochID, err := domain.NewID()
	if err != nil {
		return plan, err
	}
	epoch := domain.Epoch{ID: epochID, SourceID: req.Source.ID, BaseCursor: req.Base, Status: domain.EpochPlanned, CreatedAt: time.Now()}
	ns := "npm:" + epoch.ID
	if err = req.Workset.Reset(ctx, ns); err != nil {
		return plan, err
	}
	if err = sink.Begin(ctx, epoch, req.CapsuleID); err != nil {
		return plan, err
	}
	defer func() {
		if retErr != nil {
			_ = sink.Abort(context.Background(), epoch.ID, retErr)
		}
	}()

	var target int64
	if req.Base.Empty() {
		startRoot, err := a.root(ctx, cfg)
		if err != nil {
			return plan, err
		}
		if err = a.enumerateAllDocs(ctx, cfg, ns, req.Workset); err != nil {
			return plan, err
		}
		endRoot, err := a.root(ctx, cfg)
		if err != nil {
			return plan, err
		}
		target = int64(endRoot.UpdateSeq)
		if target < int64(startRoot.UpdateSeq) {
			return plan, fmt.Errorf("npm update_seq moved backwards %d -> %d: %w", startRoot.UpdateSeq, endRoot.UpdateSeq, domain.ErrConflict)
		}
		if err = a.applyChanges(ctx, cfg, ns, req.Workset, int64(startRoot.UpdateSeq), target); err != nil {
			return plan, err
		}
	} else {
		root, err := a.root(ctx, cfg)
		if err != nil {
			return plan, err
		}
		target = int64(root.UpdateSeq)
		if target < base {
			return plan, fmt.Errorf("npm update_seq %d is behind base %d: %w", target, base, domain.ErrConflict)
		}
		if err = a.applyChanges(ctx, cfg, ns, req.Workset, base, target); err != nil {
			return plan, err
		}
	}
	epoch.TargetCursor = domain.Cursor{Kind: "npm-update-seq", Value: strconv.FormatInt(target, 10)}

	var units, objects, metadata, totalBytes int64
	err = req.Workset.WalkValues(ctx, ns, func(name, value string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		work, err := decodeWork(value)
		if err != nil {
			return fmt.Errorf("decode npm workset %s: %w", name, err)
		}
		u, o, m, b, err := a.planPackage(ctx, req, sink, epoch, name, work)
		if err != nil {
			return err
		}
		units += u
		objects += o
		metadata += m
		totalBytes += b
		return nil
	})
	if err != nil {
		return plan, err
	}

	epoch.TotalBytes = totalBytes
	epoch.TotalObjects = objects
	epoch.PublishUnitCount = units
	plan = domain.EpochPlan{Epoch: epoch, ArtifactCount: objects, PublishUnitCount: units, MetadataCount: metadata}
	if err = sink.Commit(ctx, plan); err != nil {
		return plan, err
	}
	return plan, nil
}

func (*Adapter) MaterializeMetadata(context.Context, ports.MetadataRequest) ([]domain.GeneratedFile, error) {
	// npm packument 在 Analyze 时基于当次 upstream document 生成，不能在发布期重新拉公网。
	return nil, nil
}

func (*Adapter) ValidatePublish(ctx context.Context, req ports.PublishValidationRequest) error {
	_ = ctx
	if req.Unit.Imported != req.Unit.Required {
		return fmt.Errorf("npm publish unit %s incomplete %d/%d: %w", req.Unit.Key, req.Unit.Imported, req.Unit.Required, domain.ErrConflict)
	}
	return nil
}

var _ ports.SourceAdapter = (*Adapter)(nil)
