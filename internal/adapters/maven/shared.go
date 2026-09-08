package maven

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

const (
	GenericProvider = "maven-generic"
	CentralProvider = "maven-central"
	emptySHA256     = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

func httpClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return http.DefaultClient
}

func validateHTTPURL(name, raw string) error {
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL: %w", name, domain.ErrInvalid)
	}
	return nil
}

func resolveURL(base, ref string) (string, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("invalid Maven artifact URL %q: %w", ref, domain.ErrInvalid)
	}
	if !u.IsAbs() {
		b, err := url.Parse(strings.TrimRight(base, "/") + "/")
		if err != nil {
			return "", err
		}
		u = b.ResolveReference(u)
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("Maven artifact URL must resolve to HTTP(S): %w", domain.ErrInvalid)
	}
	u.Fragment = ""
	return u.String(), nil
}

func validHexDigest(s string, bytes int) bool {
	if len(s) != bytes*2 {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == bytes
}

func integrityFromHex(algorithm, value string) (string, error) {
	b, err := hex.DecodeString(value)
	if err != nil {
		return "", err
	}
	return strings.ToLower(algorithm) + "-" + base64.StdEncoding.EncodeToString(b), nil
}

func deterministicUnitID(epochID, key string) string {
	sum := sha256.Sum256([]byte(epochID + "\x00" + key))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func newEpoch(source domain.Source, base, target domain.Cursor) (domain.Epoch, error) {
	id, err := domain.NewID()
	if err != nil {
		return domain.Epoch{}, err
	}
	return domain.Epoch{ID: id, SourceID: source.ID, BaseCursor: base, TargetCursor: target, Status: domain.EpochPlanned, CreatedAt: time.Now()}, nil
}

func validateBaseCursor(base domain.Cursor, kind string) error {
	if base.Empty() {
		return nil
	}
	if base.Kind != kind {
		return fmt.Errorf("expected %s cursor, got %s: %w", kind, base.Kind, domain.ErrInvalid)
	}
	return nil
}

func validatePublish(req ports.PublishValidationRequest) error {
	if req.Unit.Imported != req.Unit.Required {
		return fmt.Errorf("Maven publish unit %s incomplete %d/%d: %w", req.Unit.Key, req.Unit.Imported, req.Unit.Required, domain.ErrConflict)
	}
	return nil
}

func cleanLogicalPath(raw string) (string, error) {
	clean := strings.TrimPrefix(path.Clean("/"+strings.TrimSpace(raw)), "/")
	if clean == "" || clean == "." || clean != strings.TrimSpace(raw) {
		return "", fmt.Errorf("invalid Maven logical path %q: %w", raw, domain.ErrInvalid)
	}
	if strings.Contains(clean, "\\") || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("invalid Maven logical path %q: %w", raw, domain.ErrInvalid)
	}
	return clean, nil
}

func rawAttrs(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}
