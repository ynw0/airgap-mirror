package maven

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"unicode"

	"github.com/ynw0/airgap-mirror/internal/adapters/common"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

const centralCursorKind = "maven-central-dependency-set"

type CentralAdapter struct{ Client *http.Client }

func NewCentral(client *http.Client) *CentralAdapter { return &CentralAdapter{Client: client} }
func (*CentralAdapter) Type() domain.SourceType      { return domain.SourceMaven }
func (*CentralAdapter) Provider() string             { return CentralProvider }

type centralCoordinate struct {
	GroupID    string `json:"groupId"`
	ArtifactID string `json:"artifactId"`
	Version    string `json:"version"`
	Packaging  string `json:"packaging,omitempty"`
	Classifier string `json:"classifier,omitempty"`
}

type centralConfig struct {
	Mode        string              `json:"mode"`
	Coordinates []centralCoordinate `json:"coordinates"`
}

func safeMavenToken(v string, allowDot bool) bool {
	if v == "" || strings.TrimSpace(v) != v || v == "." || v == ".." {
		return false
	}
	for _, r := range v {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '+' || (allowDot && r == '.') {
			continue
		}
		return false
	}
	return true
}

func validateCoordinate(c centralCoordinate) error {
	if c.GroupID == "" || strings.TrimSpace(c.GroupID) != c.GroupID {
		return fmt.Errorf("Maven Central coordinate missing groupId: %w", domain.ErrInvalid)
	}
	for _, segment := range strings.Split(c.GroupID, ".") {
		if !safeMavenToken(segment, false) {
			return fmt.Errorf("invalid Maven groupId %q: %w", c.GroupID, domain.ErrInvalid)
		}
	}
	if !safeMavenToken(c.ArtifactID, true) {
		return fmt.Errorf("invalid Maven artifactId %q: %w", c.ArtifactID, domain.ErrInvalid)
	}
	if !safeMavenToken(c.Version, true) {
		return fmt.Errorf("invalid Maven version %q: %w", c.Version, domain.ErrInvalid)
	}
	upper := strings.ToUpper(c.Version)
	if strings.HasSuffix(upper, "-SNAPSHOT") || upper == "LATEST" || upper == "RELEASE" || strings.ContainsAny(c.Version, "[](),") {
		return fmt.Errorf("Maven Central dependency-set requires an exact release version, got %q: %w", c.Version, domain.ErrInvalid)
	}
	if c.Packaging == "" {
		c.Packaging = "jar"
	}
	if !safeMavenToken(c.Packaging, true) {
		return fmt.Errorf("invalid Maven packaging %q: %w", c.Packaging, domain.ErrInvalid)
	}
	if c.Classifier != "" && !safeMavenToken(c.Classifier, true) {
		return fmt.Errorf("invalid Maven classifier %q: %w", c.Classifier, domain.ErrInvalid)
	}
	return nil
}

func canonicalCoordinate(c centralCoordinate) centralCoordinate {
	c.GroupID = strings.TrimSpace(c.GroupID)
	c.ArtifactID = strings.TrimSpace(c.ArtifactID)
	c.Version = strings.TrimSpace(c.Version)
	c.Packaging = strings.TrimSpace(c.Packaging)
	c.Classifier = strings.TrimSpace(c.Classifier)
	if c.Packaging == "" {
		c.Packaging = "jar"
	}
	return c
}

func coordinateKey(c centralCoordinate) string {
	return c.GroupID + ":" + c.ArtifactID + ":" + c.Packaging + ":" + c.Classifier + ":" + c.Version
}

func gavKey(c centralCoordinate) string {
	return c.GroupID + ":" + c.ArtifactID + ":" + c.Version
}

func parseCentralConfig(source domain.Source) (centralConfig, error) {
	var cfg centralConfig
	if err := json.Unmarshal(source.ConfigJSON, &cfg); err != nil {
		return cfg, fmt.Errorf("decode Maven Central config: %w", err)
	}
	if cfg.Mode != "dependency-set" {
		return cfg, fmt.Errorf("maven-central only supports mode=dependency-set: %w", domain.ErrInvalid)
	}
	if len(cfg.Coordinates) == 0 {
		return cfg, fmt.Errorf("maven-central dependency-set requires coordinates: %w", domain.ErrInvalid)
	}
	unique := make(map[string]centralCoordinate, len(cfg.Coordinates))
	for _, raw := range cfg.Coordinates {
		c := canonicalCoordinate(raw)
		if err := validateCoordinate(c); err != nil {
			return cfg, err
		}
		unique[coordinateKey(c)] = c
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	cfg.Coordinates = cfg.Coordinates[:0]
	for _, key := range keys {
		cfg.Coordinates = append(cfg.Coordinates, unique[key])
	}
	return cfg, nil
}

func (a *CentralAdapter) ValidateConfig(ctx context.Context, source domain.Source) error {
	_ = ctx
	if source.Type != domain.SourceMaven || source.Provider != CentralProvider {
		return fmt.Errorf("source must be maven/%s: %w", CentralProvider, domain.ErrInvalid)
	}
	if err := validateHTTPURL("upstreamUrl", source.UpstreamURL); err != nil {
		return err
	}
	_, err := parseCentralConfig(source)
	return err
}

func (a *CentralAdapter) Probe(ctx context.Context, source domain.Source) (domain.UpstreamState, error) {
	if err := a.ValidateConfig(ctx, source); err != nil {
		return domain.UpstreamState{}, err
	}
	cfg, _ := parseCentralConfig(source)
	h := sha256.New()
	_, _ = h.Write([]byte(strings.TrimRight(source.UpstreamURL, "/")))
	for _, c := range cfg.Coordinates {
		_, _ = h.Write([]byte{'\n'})
		_, _ = h.Write([]byte(coordinateKey(c)))
	}
	return domain.UpstreamState{Cursor: domain.Cursor{Kind: centralCursorKind, Value: hex.EncodeToString(h.Sum(nil))}, Summary: fmt.Sprintf("%d exact release coordinates", len(cfg.Coordinates))}, nil
}

func centralLogicalBase(c centralCoordinate) string {
	return path.Join(strings.ReplaceAll(c.GroupID, ".", "/"), c.ArtifactID, c.Version)
}

func centralFileName(c centralCoordinate) string {
	name := c.ArtifactID + "-" + c.Version
	if c.Classifier != "" {
		name += "-" + c.Classifier
	}
	return name + "." + c.Packaging
}

func centralURL(base, logical string) (string, error) {
	if _, err := cleanLogicalPath(logical); err != nil {
		return "", err
	}
	// Central coordinates are validated to a URL-path-safe subset, so joining the
	// canonical logical path directly avoids double-encoding already escaped bytes.
	return strings.TrimRight(base, "/") + "/" + logical, nil
}

type centralAttrs struct {
	GroupID      string `json:"groupId"`
	ArtifactID   string `json:"artifactId"`
	Version      string `json:"version"`
	Packaging    string `json:"packaging"`
	Classifier   string `json:"classifier,omitempty"`
	UpstreamSHA1 string `json:"upstreamSha1,omitempty"`
}

func catalogCentralSHA1(entry domain.CatalogEntry) string {
	var attrs centralAttrs
	if len(entry.Attributes) == 0 || json.Unmarshal(entry.Attributes, &attrs) != nil {
		return ""
	}
	return strings.ToLower(attrs.UpstreamSHA1)
}

func (a *CentralAdapter) upstreamSHA1(ctx context.Context, rawURL string) (string, []byte, string, error) {
	sidecarURL := rawURL + ".sha1"
	resp, err := common.Do(ctx, a.Client, http.MethodGet, sidecarURL, "text/plain")
	if err != nil {
		return "", nil, "", err
	}
	defer resp.Body.Close()
	body, bodySHA, err := common.ReadAllSHA256(resp.Body, 4<<10)
	if err != nil {
		return "", nil, "", err
	}
	fields := strings.Fields(string(body))
	if len(fields) != 1 {
		return "", nil, "", fmt.Errorf("Maven Central checksum %s must contain one SHA-1 token: %w", sidecarURL, domain.ErrInvalid)
	}
	sha := strings.ToLower(fields[0])
	if !validHexDigest(sha, sha1.Size) {
		return "", nil, "", fmt.Errorf("invalid Maven Central SHA-1 %q: %w", fields[0], domain.ErrInvalid)
	}
	return sha, body, bodySHA, nil
}

func (a *CentralAdapter) headSize(ctx context.Context, rawURL string) (int64, error) {
	resp, err := common.Do(ctx, a.Client, http.MethodHead, rawURL, "")
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	if resp.ContentLength < 0 {
		return 0, fmt.Errorf("Maven Central HEAD missing Content-Length for %s: %w", rawURL, domain.ErrInvalid)
	}
	return resp.ContentLength, nil
}

func (a *CentralAdapter) planResource(ctx context.Context, req ports.AnalyzeRequest, epoch domain.Epoch, c centralCoordinate, logical string) ([]domain.Artifact, error) {
	rawURL, err := centralURL(req.Source.UpstreamURL, logical)
	if err != nil {
		return nil, err
	}
	upstreamSHA1, sidecarBody, sidecarSHA256, err := a.upstreamSHA1(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("Maven Central %s checksum: %w", logical, err)
	}
	attrs, err := rawAttrs(centralAttrs{GroupID: c.GroupID, ArtifactID: c.ArtifactID, Version: c.Version, Packaging: c.Packaging, Classifier: c.Classifier, UpstreamSHA1: upstreamSHA1})
	if err != nil {
		return nil, err
	}
	unitID := deterministicUnitID(epoch.ID, gavKey(c))
	var planned []domain.Artifact

	existing, getErr := req.Catalog.Get(ctx, req.Source.ID, logical)
	if getErr == nil {
		oldSHA1 := catalogCentralSHA1(existing)
		if oldSHA1 == "" {
			return nil, fmt.Errorf("existing Maven Central artifact %s lacks upstreamSha1; inventory is required before incremental sync: %w", logical, domain.ErrConflict)
		}
		if oldSHA1 != upstreamSHA1 {
			return nil, fmt.Errorf("Maven Central immutable artifact %s changed SHA-1 %s -> %s: %w", logical, oldSHA1, upstreamSHA1, domain.ErrConflict)
		}
	} else if errors.Is(getErr, domain.ErrNotFound) {
		size, err := a.headSize(ctx, rawURL)
		if err != nil {
			return nil, err
		}
		integrity, err := integrityFromHex("sha1", upstreamSHA1)
		if err != nil {
			return nil, err
		}
		id, err := domain.NewID()
		if err != nil {
			return nil, err
		}
		planned = append(planned, domain.Artifact{ID: id, EpochID: epoch.ID, SourceID: req.Source.ID, LogicalPath: logical, Size: size, UpstreamURL: rawURL, UpstreamIntegrity: integrity, Operation: domain.ArtifactAdd, PublishUnitID: unitID, PackageKey: gavKey(c), Version: c.Version, Attributes: attrs})
	} else {
		return nil, getErr
	}

	sidecarLogical := logical + ".sha1"
	sidecarExisting, sideErr := req.Catalog.Get(ctx, req.Source.ID, sidecarLogical)
	if sideErr == nil {
		if sidecarExisting.Size != int64(len(sidecarBody)) || !strings.EqualFold(sidecarExisting.SHA256, sidecarSHA256) {
			return nil, fmt.Errorf("Maven Central immutable checksum sidecar %s changed: %w", sidecarLogical, domain.ErrConflict)
		}
	} else if errors.Is(sideErr, domain.ErrNotFound) {
		integrity, err := integrityFromHex("sha256", sidecarSHA256)
		if err != nil {
			return nil, err
		}
		id, err := domain.NewID()
		if err != nil {
			return nil, err
		}
		planned = append(planned, domain.Artifact{ID: id, EpochID: epoch.ID, SourceID: req.Source.ID, LogicalPath: sidecarLogical, Size: int64(len(sidecarBody)), SHA256: sidecarSHA256, UpstreamURL: rawURL + ".sha1", UpstreamIntegrity: integrity, Operation: domain.ArtifactAdd, PublishUnitID: unitID, PackageKey: gavKey(c), Version: c.Version, Metadata: true, Attributes: attrs})
	} else {
		return nil, sideErr
	}
	return planned, nil
}

func (a *CentralAdapter) Analyze(ctx context.Context, req ports.AnalyzeRequest, sink ports.PlanSink) (plan domain.EpochPlan, retErr error) {
	if sink == nil || req.Catalog == nil {
		return plan, fmt.Errorf("Maven Central analysis requires sink and catalog: %w", domain.ErrInvalid)
	}
	if err := a.ValidateConfig(ctx, req.Source); err != nil {
		return plan, err
	}
	if err := validateBaseCursor(req.Base, centralCursorKind); err != nil {
		return plan, err
	}
	cfg, _ := parseCentralConfig(req.Source)
	state, err := a.Probe(ctx, req.Source)
	if err != nil {
		return plan, err
	}
	epoch, err := newEpoch(req.Source, req.Base, state.Cursor)
	if err != nil {
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
	if req.Base.Equal(state.Cursor) {
		plan = domain.EpochPlan{Epoch: epoch}
		if err = sink.Commit(ctx, plan); err != nil {
			return plan, err
		}
		return plan, nil
	}

	type unitPlan struct {
		key       string
		artifacts map[string]domain.Artifact
	}
	units := map[string]*unitPlan{}
	processed := map[string]bool{}
	for _, c := range cfg.Coordinates {
		if err = ctx.Err(); err != nil {
			return plan, err
		}
		base := centralLogicalBase(c)
		pom := path.Join(base, c.ArtifactID+"-"+c.Version+".pom")
		requested := path.Join(base, centralFileName(c))
		for _, logical := range []string{pom, requested} {
			if processed[logical] {
				continue
			}
			processed[logical] = true
			artifacts, err := a.planResource(ctx, req, epoch, c, logical)
			if err != nil {
				return plan, err
			}
			if len(artifacts) == 0 {
				continue
			}
			key := gavKey(c)
			u := units[key]
			if u == nil {
				u = &unitPlan{key: key, artifacts: map[string]domain.Artifact{}}
				units[key] = u
			}
			for _, artifact := range artifacts {
				if existing, ok := u.artifacts[artifact.LogicalPath]; ok {
					if existing.Size != artifact.Size || existing.SHA256 != artifact.SHA256 || existing.UpstreamIntegrity != artifact.UpstreamIntegrity {
						return plan, fmt.Errorf("Maven Central path %s planned inconsistently: %w", artifact.LogicalPath, domain.ErrConflict)
					}
					continue
				}
				u.artifacts[artifact.LogicalPath] = artifact
			}
		}
	}

	unitKeys := make([]string, 0, len(units))
	for key := range units {
		unitKeys = append(unitKeys, key)
	}
	sort.Strings(unitKeys)
	var objectCount, metadataCount, totalBytes int64
	for _, key := range unitKeys {
		u := units[key]
		paths := make([]string, 0, len(u.artifacts))
		for logical := range u.artifacts {
			paths = append(paths, logical)
		}
		sort.Strings(paths)
		for _, logical := range paths {
			artifact := u.artifacts[logical]
			if err = sink.PutArtifact(ctx, artifact); err != nil {
				return plan, err
			}
			objectCount++
			totalBytes += artifact.Size
			if artifact.Metadata {
				metadataCount++
			}
		}
		pu := domain.PublishUnit{ID: deterministicUnitID(epoch.ID, key), EpochID: epoch.ID, SourceID: req.Source.ID, Key: key, Status: domain.PublishWaiting, Required: int64(len(paths))}
		if err = sink.PutPublishUnit(ctx, pu); err != nil {
			return plan, err
		}
	}

	epoch.TotalBytes = totalBytes
	epoch.TotalObjects = objectCount
	epoch.PublishUnitCount = int64(len(unitKeys))
	plan = domain.EpochPlan{Epoch: epoch, ArtifactCount: objectCount, PublishUnitCount: int64(len(unitKeys)), MetadataCount: metadataCount}
	if err = sink.Commit(ctx, plan); err != nil {
		return plan, err
	}
	return plan, nil
}

func (*CentralAdapter) MaterializeMetadata(context.Context, ports.MetadataRequest) ([]domain.GeneratedFile, error) {
	return nil, nil
}

func (*CentralAdapter) ValidatePublish(ctx context.Context, req ports.PublishValidationRequest) error {
	_ = ctx
	return validatePublish(req)
}

var _ ports.SourceAdapter = (*CentralAdapter)(nil)
