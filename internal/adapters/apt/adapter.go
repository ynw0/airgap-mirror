package apt

import (
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
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

	"github.com/ulikunitz/xz"

	"github.com/ynw0/airgap-mirror/internal/adapters/common"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

const Provider = "apt-generic"

type Config struct {
	Suite       string `json:"suite"`
	ReleaseMode string `json:"releaseMode"`
}

type Adapter struct{ Client *http.Client }

type releaseSnapshot struct {
	Release   []byte
	Plain     []byte
	Signature []byte
	Cursor    string
}

func New(client *http.Client) *Adapter   { return &Adapter{Client: client} }
func (*Adapter) Type() domain.SourceType { return domain.SourceAPT }
func (*Adapter) Provider() string        { return Provider }

func parseConfig(source domain.Source) (Config, error) {
	var cfg Config
	if len(source.ConfigJSON) == 0 {
		return cfg, fmt.Errorf("apt config is required: %w", domain.ErrInvalid)
	}
	if err := json.Unmarshal(source.ConfigJSON, &cfg); err != nil {
		return cfg, fmt.Errorf("decode apt config: %w", err)
	}
	cfg.Suite = strings.Trim(cfg.Suite, "/")
	if cfg.Suite == "" || strings.Contains(cfg.Suite, "..") {
		return cfg, fmt.Errorf("suite is required and must be a relative suite name: %w", domain.ErrInvalid)
	}
	if cfg.ReleaseMode != "inrelease" && cfg.ReleaseMode != "detached" {
		return cfg, fmt.Errorf("releaseMode must be inrelease or detached: %w", domain.ErrInvalid)
	}
	return cfg, nil
}

func (a *Adapter) ValidateConfig(ctx context.Context, source domain.Source) error {
	_ = ctx
	if source.Type != domain.SourceAPT || source.Provider != Provider {
		return fmt.Errorf("source must be apt/%s: %w", Provider, domain.ErrInvalid)
	}
	if source.UpstreamURL == "" || source.PublicURL == "" {
		return fmt.Errorf("upstreamUrl and publicUrl are required: %w", domain.ErrInvalid)
	}
	for name, raw := range map[string]string{"upstreamUrl": source.UpstreamURL, "publicUrl": source.PublicURL} {
		u, err := url.ParseRequestURI(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("%s must be an absolute HTTP(S) URL: %w", name, domain.ErrInvalid)
		}
	}
	_, err := parseConfig(source)
	return err
}

func joinURL(base string, elems ...string) string {
	base = strings.TrimRight(base, "/")
	parts := make([]string, 0, len(elems))
	for _, e := range elems {
		parts = append(parts, strings.Trim(e, "/"))
	}
	return base + "/" + path.Join(parts...)
}

func (a *Adapter) release(ctx context.Context, source domain.Source, cfg Config) (releaseSnapshot, error) {
	var snap releaseSnapshot
	var releaseURL string
	if cfg.ReleaseMode == "inrelease" {
		releaseURL = joinURL(source.UpstreamURL, "dists", cfg.Suite, "InRelease")
	} else {
		releaseURL = joinURL(source.UpstreamURL, "dists", cfg.Suite, "Release")
	}
	resp, err := common.Do(ctx, a.Client, http.MethodGet, releaseURL, "")
	if err != nil {
		return snap, err
	}
	body, releaseSHA, err := common.ReadAllSHA256(resp.Body, 32<<20)
	resp.Body.Close()
	if err != nil {
		return snap, err
	}
	snap.Release = body
	if cfg.ReleaseMode == "inrelease" {
		plain, err := clearSignedBody(body)
		if err != nil {
			return snap, err
		}
		snap.Plain = plain
		snap.Cursor = releaseSHA
		return snap, nil
	}
	snap.Plain = body
	sigURL := joinURL(source.UpstreamURL, "dists", cfg.Suite, "Release.gpg")
	resp, err = common.Do(ctx, a.Client, http.MethodGet, sigURL, "")
	if err != nil {
		return snap, err
	}
	sig, sigSHA, err := common.ReadAllSHA256(resp.Body, 8<<20)
	resp.Body.Close()
	if err != nil {
		return snap, err
	}
	snap.Signature = sig
	h := sha256.New()
	_, _ = h.Write([]byte(releaseSHA))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(sigSHA))
	snap.Cursor = hex.EncodeToString(h.Sum(nil))
	return snap, nil
}

func (a *Adapter) Probe(ctx context.Context, source domain.Source) (domain.UpstreamState, error) {
	cfg, err := parseConfig(source)
	if err != nil {
		return domain.UpstreamState{}, err
	}
	snap, err := a.release(ctx, source, cfg)
	if err != nil {
		return domain.UpstreamState{}, err
	}
	return domain.UpstreamState{Cursor: domain.Cursor{Kind: "apt-release-snapshot-sha256", Value: snap.Cursor}, ObservedAt: time.Now(), Summary: cfg.Suite}, nil
}

type releaseEntry struct {
	Path   string
	Size   int64
	SHA256 string
}

type releaseDoc struct {
	AcquireByHash bool
	Entries       []releaseEntry
}

func clearSignedBody(in []byte) ([]byte, error) {
	s := strings.ReplaceAll(string(in), "\r\n", "\n")
	if !strings.HasPrefix(s, "-----BEGIN PGP SIGNED MESSAGE-----\n") {
		return nil, fmt.Errorf("InRelease is not an OpenPGP clear-signed message: %w", domain.ErrInvalid)
	}
	start := strings.Index(s, "\n\n")
	if start < 0 {
		return nil, fmt.Errorf("InRelease cleartext header is incomplete: %w", domain.ErrInvalid)
	}
	body := s[start+2:]
	end := strings.Index(body, "\n-----BEGIN PGP SIGNATURE-----")
	if end < 0 {
		return nil, fmt.Errorf("InRelease signature block missing: %w", domain.ErrInvalid)
	}
	lines := strings.Split(body[:end], "\n")
	for i := range lines {
		if strings.HasPrefix(lines[i], "- ") {
			lines[i] = strings.TrimPrefix(lines[i], "- ")
		}
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func parseRelease(body []byte) (releaseDoc, error) {
	var out releaseDoc
	s := bufio.NewScanner(strings.NewReader(string(body)))
	buf := make([]byte, 64<<10)
	s.Buffer(buf, 2<<20)
	inSHA := false
	seenSHA := false
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "Acquire-By-Hash:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "Acquire-By-Hash:"))
			out.AcquireByHash = strings.EqualFold(v, "yes") || strings.EqualFold(v, "true")
		}
		if line == "SHA256:" {
			inSHA = true
			seenSHA = true
			continue
		}
		if inSHA {
			if len(line) == 0 || (line[0] != ' ' && line[0] != '\t') {
				inSHA = false
			} else {
				fields := strings.Fields(line)
				if len(fields) != 3 {
					return out, fmt.Errorf("invalid Release SHA256 row %q: %w", line, domain.ErrInvalid)
				}
				if len(fields[0]) != 64 {
					return out, fmt.Errorf("invalid Release sha256 %q: %w", fields[0], domain.ErrInvalid)
				}
				if _, err := hex.DecodeString(fields[0]); err != nil {
					return out, fmt.Errorf("invalid Release sha256 %q: %w", fields[0], domain.ErrInvalid)
				}
				n, err := strconv.ParseInt(fields[1], 10, 64)
				if err != nil || n < 0 {
					return out, fmt.Errorf("invalid Release size %q: %w", fields[1], domain.ErrInvalid)
				}
				p := path.Clean(fields[2])
				if p != fields[2] || p == "." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
					return out, fmt.Errorf("unsafe Release path %q: %w", fields[2], domain.ErrInvalid)
				}
				out.Entries = append(out.Entries, releaseEntry{Path: p, Size: n, SHA256: strings.ToLower(fields[0])})
				continue
			}
		}
	}
	if err := s.Err(); err != nil {
		return out, err
	}
	if !seenSHA || len(out.Entries) == 0 {
		return out, fmt.Errorf("Release has no SHA256 entries: %w", domain.ErrInvalid)
	}
	return out, nil
}

func baseIndexName(p string) (string, bool, bool) {
	for _, suffix := range []string{".xz", ".gz", ".bz2"} {
		p = strings.TrimSuffix(p, suffix)
	}
	if strings.HasSuffix(p, "/Packages") {
		return p, true, false
	}
	if strings.HasSuffix(p, "/Sources") {
		return p, false, true
	}
	return "", false, false
}

func compressionRank(p string) int {
	switch {
	case strings.HasSuffix(p, ".xz"):
		return 4
	case strings.HasSuffix(p, ".gz"):
		return 3
	case strings.HasSuffix(p, ".bz2"):
		return 2
	default:
		return 1
	}
}

func pickParseIndexes(entries []releaseEntry) []releaseEntry {
	best := map[string]releaseEntry{}
	for _, e := range entries {
		base, pkg, src := baseIndexName(e.Path)
		if !pkg && !src {
			continue
		}
		if old, ok := best[base]; !ok || compressionRank(e.Path) > compressionRank(old.Path) {
			best[base] = e
		}
	}
	out := make([]releaseEntry, 0, len(best))
	for _, e := range best {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func decompressor(name string, r io.Reader) (io.Reader, io.Closer, error) {
	switch {
	case strings.HasSuffix(name, ".xz"):
		xr, err := xz.NewReader(r)
		return xr, nil, err
	case strings.HasSuffix(name, ".gz"):
		gr, err := gzip.NewReader(r)
		return gr, gr, err
	case strings.HasSuffix(name, ".bz2"):
		return bzip2.NewReader(r), nil, nil
	default:
		return r, nil, nil
	}
}

func parseDeb822(r io.Reader, fn func(map[string]string) error) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64<<10), 16<<20)
	rec := map[string]string{}
	last := ""
	flush := func() error {
		if len(rec) == 0 {
			return nil
		}
		if err := fn(rec); err != nil {
			return err
		}
		rec = map[string]string{}
		last = ""
		return nil
	}
	for s.Scan() {
		line := s.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && last != "" {
			rec[last] += "\n" + strings.TrimSpace(line)
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx <= 0 {
			return fmt.Errorf("invalid deb822 line %q: %w", line, domain.ErrInvalid)
		}
		last = line[:idx]
		rec[last] = strings.TrimSpace(line[idx+1:])
	}
	if err := s.Err(); err != nil {
		return err
	}
	return flush()
}

func validSHA256(v string) bool {
	if len(v) != 64 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}

func (a *Adapter) Analyze(ctx context.Context, req ports.AnalyzeRequest, sink ports.PlanSink) (plan domain.EpochPlan, err error) {
	cfg, err := parseConfig(req.Source)
	if err != nil {
		return plan, err
	}
	snap, err := a.release(ctx, req.Source, cfg)
	if err != nil {
		return plan, err
	}
	doc, err := parseRelease(snap.Plain)
	if err != nil {
		return plan, err
	}
	epochID, err := domain.NewID()
	if err != nil {
		return plan, err
	}
	unitID, err := domain.NewID()
	if err != nil {
		return plan, err
	}
	epoch := domain.Epoch{ID: epochID, SourceID: req.Source.ID, BaseCursor: req.Base, TargetCursor: domain.Cursor{Kind: "apt-release-snapshot-sha256", Value: snap.Cursor}, Status: domain.EpochPlanned, CreatedAt: time.Now(), PublishUnitCount: 1}
	plan.Epoch = epoch
	plan.PublishUnitCount = 1
	if err = sink.Begin(ctx, epoch, req.CapsuleID); err != nil {
		return plan, err
	}
	committed := false
	defer func() {
		if err != nil && !committed {
			_ = sink.Abort(context.Background(), epochID, err)
		}
	}()
	unit := domain.PublishUnit{ID: unitID, EpochID: epochID, SourceID: req.Source.ID, Key: "apt:" + cfg.Suite, Status: domain.PublishWaiting}
	if err = sink.PutPublishUnit(ctx, unit); err != nil {
		return plan, err
	}
	ns := "apt:" + epochID + ":paths"
	if req.Workset == nil || req.Catalog == nil {
		return plan, fmt.Errorf("apt analyze requires disk workset and catalog: %w", domain.ErrInvalid)
	}
	if err = req.Workset.Reset(ctx, ns); err != nil {
		return plan, err
	}
	classify := func(art domain.Artifact) (domain.ArtifactOperation, bool, error) {
		existing, e := req.Catalog.Get(ctx, req.Source.ID, art.LogicalPath)
		if e == nil {
			if existing.Size == art.Size && strings.EqualFold(existing.SHA256, art.SHA256) {
				return domain.ArtifactAdd, false, nil
			}
			if !art.Metadata {
				return "", false, fmt.Errorf("APT immutable path changed %s: %w", art.LogicalPath, domain.ErrConflict)
			}
			return domain.ArtifactUpdate, true, nil
		}
		if !errors.Is(e, domain.ErrNotFound) {
			return "", false, e
		}
		return domain.ArtifactAdd, true, nil
	}
	var required, metadataCount, totalBytes int64
	add := func(art domain.Artifact) error {
		fresh, e := req.Workset.Add(ctx, ns, art.LogicalPath)
		if e != nil {
			return e
		}
		if !fresh {
			return nil
		}
		op, needed, e := classify(art)
		if e != nil {
			return e
		}
		if !needed {
			return nil
		}
		if art.ID == "" {
			art.ID, e = domain.NewID()
			if e != nil {
				return e
			}
		}
		art.EpochID = epochID
		art.SourceID = req.Source.ID
		art.PublishUnitID = unitID
		art.Operation = op
		if e = sink.PutArtifact(ctx, art); e != nil {
			return e
		}
		required++
		totalBytes += art.Size
		plan.ArtifactCount++
		if art.Metadata {
			metadataCount++
		}
		return nil
	}
	distBase := path.Join("dists", cfg.Suite)
	if cfg.ReleaseMode == "inrelease" {
		if err = add(domain.Artifact{LogicalPath: path.Join(distBase, "InRelease"), Size: int64(len(snap.Release)), SHA256: shaHex(snap.Release), UpstreamURL: joinURL(req.Source.UpstreamURL, distBase, "InRelease"), Metadata: true}); err != nil {
			return plan, err
		}
	} else {
		if err = add(domain.Artifact{LogicalPath: path.Join(distBase, "Release"), Size: int64(len(snap.Release)), SHA256: shaHex(snap.Release), UpstreamURL: joinURL(req.Source.UpstreamURL, distBase, "Release"), Metadata: true}); err != nil {
			return plan, err
		}
		if err = add(domain.Artifact{LogicalPath: path.Join(distBase, "Release.gpg"), Size: int64(len(snap.Signature)), SHA256: shaHex(snap.Signature), UpstreamURL: joinURL(req.Source.UpstreamURL, distBase, "Release.gpg"), Metadata: true}); err != nil {
			return plan, err
		}
	}
	for _, e := range doc.Entries {
		logical := path.Join(distBase, e.Path)
		if err = add(domain.Artifact{LogicalPath: logical, Size: e.Size, SHA256: e.SHA256, UpstreamURL: joinURL(req.Source.UpstreamURL, logical), Metadata: true}); err != nil {
			return plan, err
		}
		if doc.AcquireByHash {
			byHash := path.Join(path.Dir(logical), "by-hash", "SHA256", e.SHA256)
			if err = add(domain.Artifact{LogicalPath: byHash, Size: e.Size, SHA256: e.SHA256, UpstreamURL: joinURL(req.Source.UpstreamURL, byHash), Metadata: true}); err != nil {
				return plan, err
			}
		}
	}
	for _, index := range pickParseIndexes(doc.Entries) {
		logical := path.Join(distBase, index.Path)
		_, needsParse, e := classify(domain.Artifact{LogicalPath: logical, Size: index.Size, SHA256: index.SHA256, Metadata: true})
		if e != nil {
			return plan, e
		}
		if !needsParse {
			continue
		}
		resp, e := common.Do(ctx, a.Client, http.MethodGet, joinURL(req.Source.UpstreamURL, logical), "")
		if e != nil {
			return plan, e
		}
		h := sha256.New()
		limited := io.LimitReader(resp.Body, index.Size+1)
		tee := io.TeeReader(limited, h)
		dec, closer, e := decompressor(index.Path, tee)
		if e != nil {
			resp.Body.Close()
			return plan, e
		}
		_, pkg, src := baseIndexName(index.Path)
		e = parseDeb822(dec, func(rec map[string]string) error {
			if pkg {
				filename := rec["Filename"]
				sizeText := rec["Size"]
				sum := strings.ToLower(rec["SHA256"])
				if filename == "" || sizeText == "" || !validSHA256(sum) {
					return fmt.Errorf("Packages record missing Filename/Size/SHA256: %w", domain.ErrInvalid)
				}
				size, x := strconv.ParseInt(sizeText, 10, 64)
				if x != nil || size < 0 {
					return domain.ErrInvalid
				}
				p := path.Clean(filename)
				if p != filename || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
					return domain.ErrInvalid
				}
				return add(domain.Artifact{LogicalPath: p, Size: size, SHA256: sum, UpstreamURL: joinURL(req.Source.UpstreamURL, p), PackageKey: rec["Package"], Version: rec["Version"]})
			}
			if src {
				dir := rec["Directory"]
				checks := rec["Checksums-Sha256"]
				if dir == "" || checks == "" {
					return fmt.Errorf("Sources record missing Directory/Checksums-Sha256: %w", domain.ErrInvalid)
				}
				for _, line := range strings.Split(checks, "\n") {
					f := strings.Fields(line)
					if len(f) != 3 || !validSHA256(f[0]) {
						return domain.ErrInvalid
					}
					size, x := strconv.ParseInt(f[1], 10, 64)
					if x != nil || size < 0 {
						return domain.ErrInvalid
					}
					lp := path.Clean(path.Join(dir, f[2]))
					if strings.HasPrefix(lp, "../") || strings.HasPrefix(lp, "/") {
						return domain.ErrInvalid
					}
					if x = add(domain.Artifact{LogicalPath: lp, Size: size, SHA256: strings.ToLower(f[0]), UpstreamURL: joinURL(req.Source.UpstreamURL, lp), PackageKey: rec["Package"], Version: rec["Version"]}); x != nil {
						return x
					}
				}
			}
			return nil
		})
		if closer != nil {
			_ = closer.Close()
		}
		_, _ = io.Copy(io.Discard, tee)
		resp.Body.Close()
		if e != nil {
			return plan, e
		}
		if hex.EncodeToString(h.Sum(nil)) != index.SHA256 {
			return plan, fmt.Errorf("APT index %s sha256 mismatch: %w", index.Path, domain.ErrConflict)
		}
	}
	unit.Required = required
	if err = sink.PutPublishUnit(ctx, unit); err != nil {
		return plan, err
	}
	epoch.TotalObjects = required
	epoch.TotalBytes = totalBytes
	plan.Epoch = epoch
	plan.MetadataCount = metadataCount
	if err = sink.Commit(ctx, plan); err != nil {
		return plan, err
	}
	committed = true
	return plan, nil
}

func shaHex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (*Adapter) MaterializeMetadata(context.Context, ports.MetadataRequest) ([]domain.GeneratedFile, error) {
	return nil, nil
}
func (*Adapter) ValidatePublish(_ context.Context, req ports.PublishValidationRequest) error {
	if req.Unit.Imported != req.Unit.Required {
		return fmt.Errorf("APT suite publish unit incomplete %d/%d: %w", req.Unit.Imported, req.Unit.Required, domain.ErrConflict)
	}
	return nil
}

var _ ports.SourceAdapter = (*Adapter)(nil)
var _ = errors.Is
