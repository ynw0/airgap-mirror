package pypi

import (
	"context"
	"errors"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestValidateConfigRequiresAbsoluteHTTP(t *testing.T) {
	a := New(nil)
	src := domain.Source{Type: domain.SourcePyPI, Provider: Provider, UpstreamURL: "https://pypi.org/simple", PublicURL: "repo.internal/pypi"}
	if err := a.ValidateConfig(context.Background(), src); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("publicUrl without scheme must be invalid, got %v", err)
	}
	src.PublicURL = "https://repo.internal/pypi/simple"
	if err := a.ValidateConfig(context.Background(), src); err != nil {
		t.Fatal(err)
	}
}
