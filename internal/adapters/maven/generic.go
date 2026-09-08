package maven

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/adapters/common"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

const genericCursorKind = "maven-index-sha256"

type GenericAdapter struct{ Client *http.Client }

func NewGeneric(client *http.Client) *GenericAdapter { return &GenericAdapter{Client: client} }
func (*GenericAdapter) Type() domain.SourceType       { return domain.SourceMaven }
func (*GenericAdapter) Provider() string              { return GenericProvider }

type genericConfig struct {
	IndexURL string `json:"indexUrl"`
}

func parseGenericConfig(source domain.Source) (genericConfig, error) {
	var cfg genericConfig
	if err := json.Unmarshal(source.ConfigJSON, &cfg); err != nil {
		return cfg, fmt.Errorf("decode Maven generic config: %w", err)
	}
	cfg.IndexURL = strings.TrimSpace(cfg.IndexURL)
	if err := validateHTTPURL("indexUrl", cfg.IndexURL); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (a *GenericAdapter) ValidateConfig(ctx context.Context, source domain.Source) error {
	_ = ctx
	if source.Type != domain.SourceMaven || source.Provider != GenericProvider {
		return fmt.Errorf("source must be maven/%s: %w", GenericProvider, domain.ErrInvalid)
	}
	if err := validateHTTPURL("upstreamUrl", source.UpstreamURL); err != nil {
		return err
	}
	_, err := parseGenericConfig(source)
	return err
}

func (a *GenericAdapter) Probe(ctx context.Context, source domain.Source) (domain.UpstreamState, error) {
	if err := a.ValidateConfig(ctx, source); err != nil {
		return domain.UpstreamState{}, err
	}
	cfg, _ := parseGenericConfig(source)
	resp, err := common.Do(ctx, a.Client, http.MethodGet, cfg.IndexURL, "application/x-ndjson, application/json")
	if err != nil {
		return domain.UpstreamState{}, err
	}
	defer resp.Body.Close()
	h := sha256.New()
	if _, err = io.Copy(h, resp.Body); err != nil {
		return domain.UpstreamState{}, err
	}
	return domain.UpstreamState{Cursor: domain.Cursor{Kind: genericCursorKind, Value: hex.EncodeToString(h.Sum(nil))}}, nil
}

type genericRecord struct {
	LogicalPath  string          `json:"path"`
	URL          string          `json:"url,omitempty"`
	Size         int64           `json:"size,omitempty"`
	SHA256       string          `json:"sha256,omitempty"`
	Operation    string          `json:"operation"`
	PublishUnit  string          `json:"publishUnit"`
	MetadataPath string          `json:"metadataPath,omitempty"`
	Metadata     bool            `json:"metadata"`
	PackageKey   string          `json:"packageKey,omitempty"`
	Version      string          `json:"version,omitempty"`
	Attributes   json.RawMessage `json:"attributes,omitempty"`
}

type genericPlanned struct {
	UnitKey      string          `json:"unitKey"`
	MetadataPath string          `json:"metadataPath,omitempty"`
	Artifact     domain.Artifact `json:"artifact"`
}

func (a *GenericAdapter) planRecord(ctx context.Context, req ports.AnalyzeRequest, epoch domain.Epoch, rec genericRecord) (genericPlanned, bool, error) {
	var out genericPlanned
	logical, err := cleanLogicalPath(rec.LogicalPath)
	if err != nil {
		return out, false, err
	}
	rec.PublishUnit = strings.TrimSpace(rec.PublishUnit)
	if rec.PublishUnit == "" {
		return out, false, fmt.Errorf("Maven manifest entry %s missing publishUnit: %w", logical, domain.ErrInvalid)
	}
	if rec.MetadataPath != "" {
		if rec.MetadataPath, err = cleanLogicalPath(rec.MetadataPath); err != nil {
			return out, false, fmt.Errorf("publish unit %s metadataPath: %w", rec.PublishUnit, err)
		}
	}
	op := domain.ArtifactOperation(strings.ToUpper(strings.TrimSpace(rec.Operation)))
	existing, getErr := req.Catalog.Get(ctx, req.Source.ID, logical)

	switch op {
	case domain.ArtifactDelete:
		if !rec.Metadata {
			return out, false, fmt.Errorf("Maven physical deletion %s is reserved for GC: %w", logical, domain.ErrInvalid)
		}
		if rec.URL != "" || rec.Size != 0 || rec.SHA256 != "" {
			return out, false, fmt.Errorf("Maven metadata tombstone %s must not carry url/size/hash: %w", logical, domain.ErrInvalid)
		}
		if errors.Is(getErr, domain.ErrNotFound) {
			return out, false, nil
		}
		if getErr != nil {
			return out, false, getErr
		}
	case domain.ArtifactAdd, domain.ArtifactUpdate:
		sha := strings.ToLower(strings.TrimSpace(rec.SHA256))
		if rec.Size < 0 || !validHexDigest(sha, sha256.Size) {
			return out, false, fmt.Errorf("Maven manifest entry %s has invalid size/sha256: %w", logical, domain.ErrInvalid)
		}
		rawURL, err := resolveURL(req.Source.UpstreamURL, rec.URL)
		if err != nil {
			return out, false, err
		}
		rec.URL = rawURL
		rec.SHA256 = sha
		if getErr == nil {
			if existing.Size == rec.Size && strings.EqualFold(existing.SHA256, sha) {
				return out, false, nil
			}
			if op == domain.ArtifactAdd {
				return out, false, fmt.Errorf("Maven ADD changes existing path %s: %w", logical, domain.ErrConflict)
			}
			if !rec.Metadata {
				return out, false, fmt.Errorf("Maven non-metadata path %s is immutable; in-place UPDATE is unsupported: %w", logical, domain.ErrConflict)
			}
		} else if errors.Is(getErr, domain.ErrNotFound) {
			if op == domain.ArtifactUpdate {
				return out, false, fmt.Errorf("Maven UPDATE targets missing metadata %s: %w", logical, domain.ErrConflict)
			}
		} else {
			return out, false, getErr
		}
	default:
		return out, false, fmt.Errorf("Maven manifest entry %s has unsupported operation %q: %w", logical, rec.Operation, domain.ErrInvalid)
	}

	id, err := domain.NewID()
	if err != nil {
		return out, false, err
	}
	a := domain.Artifact{ID: id, EpochID: epoch.ID, SourceID: req.Source.ID, LogicalPath: logical, Operation: op, PublishUnitID: deterministicUnitID(epoch.ID, rec.PublishUnit), PackageKey: rec.PackageKey, Version: rec.Version, Metadata: rec.Metadata, Attributes: rec.Attributes}
	if op == domain.ArtifactDelete {
		a.Size = 0
		a.SHA256 = emptySHA256
	} else {
		integrity, err := integrityFromHex("sha256", rec.SHA256)
		if err != nil {
			return out, false, err
		}
		a.Size = rec.Size
		a.SHA256 = rec.SHA256
		a.UpstreamURL = rec.URL
		a.UpstreamIntegrity = integrity
	}
	out = genericPlanned{UnitKey: rec.PublishUnit, MetadataPath: rec.MetadataPath, Artifact: a}
	return out, true, nil
}

func genericWorkKey(unit, logical string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(unit)) + ":" + base64.RawURLEncoding.EncodeToString([]byte(logical))
}

func (a *GenericAdapter) Analyze(ctx context.Context, req ports.AnalyzeRequest, sink ports.PlanSink) (plan domain.EpochPlan, retErr error) {
	if sink == nil || req.Workset == nil || req.Catalog == nil {
		return plan, fmt.Errorf("Maven generic analysis requires sink, workset and catalog: %w", domain.ErrInvalid)
	}
	if err := a.ValidateConfig(ctx, req.Source); err != nil {
		return plan, err
	}
	if err := validateBaseCursor(req.Base, genericCursorKind); err != nil {
		return plan, err
	}
	cfg, _ := parseGenericConfig(req.Source)
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

	ns := "maven-generic:" + epoch.ID
	pathNS := ns + ":paths"
	if err = req.Workset.Reset(ctx, ns); err != nil {
		return plan, err
	}
	if err = req.Workset.Reset(ctx, pathNS); err != nil {
		return plan, err
	}
	resp, err := common.Do(ctx, a.Client, http.MethodGet, cfg.IndexURL, "application/x-ndjson, application/json")
	if err != nil {
		return plan, err
	}
	defer resp.Body.Close()
	h := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(resp.Body, h))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	line := 0
	for scanner.Scan() {
		line++
		if err = ctx.Err(); err != nil {
			return plan, err
		}
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		var rec genericRecord
		if err = json.Unmarshal([]byte(raw), &rec); err != nil {
			return plan, fmt.Errorf("decode Maven manifest line %d: %w", line, err)
		}
		planned, ok, err := a.planRecord(ctx, req, epoch, rec)
		if err != nil {
			return plan, fmt.Errorf("Maven manifest line %d: %w", line, err)
		}
		if !ok {
			continue
		}
		fresh, err := req.Workset.Add(ctx, pathNS, planned.Artifact.LogicalPath)
		if err != nil {
			return plan, err
		}
		if !fresh {
			return plan, fmt.Errorf("Maven manifest plans logical path %s more than once: %w", planned.Artifact.LogicalPath, domain.ErrConflict)
		}
		body, err := json.Marshal(planned)
		if err != nil {
			return plan, err
		}
		key := genericWorkKey(planned.UnitKey, planned.Artifact.LogicalPath)
		if err = req.Workset.Put(ctx, ns, key, string(body)); err != nil {
			return plan, err
		}
	}
	if err = scanner.Err(); err != nil {
		return plan, err
	}
	actualCursor := hex.EncodeToString(h.Sum(nil))
	if actualCursor != state.Cursor.Value {
		return plan, fmt.Errorf("Maven generic index changed during analysis: probe=%s analyze=%s: %w", state.Cursor.Value, actualCursor, domain.ErrConflict)
	}

	var currentKey, currentMeta string
	var currentRequired int64
	var units, objects, metadata, totalBytes int64
	flush := func() error {
		if currentKey == "" {
			return nil
		}
		u := domain.PublishUnit{ID: deterministicUnitID(epoch.ID, currentKey), EpochID: epoch.ID, SourceID: req.Source.ID, Key: currentKey, Status: domain.PublishWaiting, Required: currentRequired, MetadataPath: currentMeta}
		if err := sink.PutPublishUnit(ctx, u); err != nil {
			return err
		}
		units++
		return nil
	}
	err = req.Workset.WalkValues(ctx, ns, func(_ string, value string) error {
		var item genericPlanned
		if err := json.Unmarshal([]byte(value), &item); err != nil {
			return err
		}
		if currentKey != "" && item.UnitKey != currentKey {
			if err := flush(); err != nil {
				return err
			}
			currentRequired = 0
			currentMeta = ""
		}
		if currentKey == "" || item.UnitKey != currentKey {
			currentKey = item.UnitKey
			currentMeta = item.MetadataPath
		} else if currentMeta != item.MetadataPath {
			return fmt.Errorf("Maven publish unit %s has inconsistent metadataPath: %w", currentKey, domain.ErrConflict)
		}
		if err := sink.PutArtifact(ctx, item.Artifact); err != nil {
			return err
		}
		currentRequired++
		objects++
		totalBytes += item.Artifact.Size
		if item.Artifact.Metadata {
			metadata++
		}
		return nil
	})
	if err != nil {
		return plan, err
	}
	if err = flush(); err != nil {
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

func (*GenericAdapter) MaterializeMetadata(context.Context, ports.MetadataRequest) ([]domain.GeneratedFile, error) {
	return nil, nil
}

func (*GenericAdapter) ValidatePublish(ctx context.Context, req ports.PublishValidationRequest) error {
	_ = ctx
	return validatePublish(req)
}

var _ ports.SourceAdapter = (*GenericAdapter)(nil)
