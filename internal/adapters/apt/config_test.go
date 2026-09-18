package apt

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestValidateConfigRequiresSuiteAndReleaseMode(t *testing.T) {
	a := New(nil)
	src := domain.Source{
		Type: domain.SourceAPT, Provider: Provider,
		UpstreamURL: "https://deb.debian.org/debian", PublicURL: "https://repo.internal/apt",
		ConfigJSON: json.RawMessage(`{"suite":"","releaseMode":"inrelease"}`),
	}
	if err := a.ValidateConfig(context.Background(), src); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty suite should be invalid, got %v", err)
	}
	src.ConfigJSON = json.RawMessage(`{"suite":"bookworm","releaseMode":"unsigned"}`)
	if err := a.ValidateConfig(context.Background(), src); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown releaseMode should be invalid, got %v", err)
	}
}

func TestManagedPathsPoolRequiresOptIn(t *testing.T) {
	a := New(nil)
	src := domain.Source{
		Type: domain.SourceAPT, Provider: Provider,
		UpstreamURL: "https://deb.debian.org/debian", PublicURL: "https://repo.internal/apt",
		ConfigJSON: json.RawMessage(`{"suite":"bookworm","releaseMode":"inrelease"}`),
	}
	matcher, err := a.ManagedPaths(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := matcher("dists/bookworm/InRelease")
	if err != nil || !owned {
		t.Fatalf("dists must be owned: %v %v", owned, err)
	}
	owned, err = matcher("pool/main/a.deb")
	if err != nil || owned {
		t.Fatalf("pool must not be owned by default: %v %v", owned, err)
	}
	src.ConfigJSON = json.RawMessage(`{"suite":"bookworm","releaseMode":"inrelease","gcOwnsPool":true}`)
	matcher, err = a.ManagedPaths(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	owned, err = matcher("pool/main/a.deb")
	if err != nil || !owned {
		t.Fatalf("gcOwnsPool should own pool: %v %v", owned, err)
	}
}
