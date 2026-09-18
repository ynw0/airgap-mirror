package npm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestValidateConfigRequiresHTTPURLs(t *testing.T) {
	a := New(nil)
	src := domain.Source{
		Type: domain.SourceNPM, Provider: Provider,
		UpstreamURL: "https://registry.npmjs.org",
		PublicURL:   "/npm/",
		ConfigJSON:  json.RawMessage(`{}`),
	}
	if err := a.ValidateConfig(context.Background(), src); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("relative publicUrl must be rejected, got %v", err)
	}
	src.PublicURL = "https://repo.internal/npm-files"
	if err := a.ValidateConfig(context.Background(), src); err != nil {
		t.Fatal(err)
	}
}
