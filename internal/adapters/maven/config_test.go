package maven

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

func TestCentralRejectsSnapshotAndRanges(t *testing.T) {
	a := NewCentral(nil)
	base := domain.Source{
		Type: domain.SourceMaven, Provider: CentralProvider,
		UpstreamURL: "https://repo1.maven.org/maven2",
		ConfigJSON:  json.RawMessage(`{"mode":"full"}`),
	}
	if err := a.ValidateConfig(context.Background(), base); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("full mirror mode must be rejected, got %v", err)
	}
	bad := []string{
		`{"mode":"dependency-set","coordinates":[{"groupId":"g","artifactId":"a","version":"1.0-SNAPSHOT"}]}`,
		`{"mode":"dependency-set","coordinates":[{"groupId":"g","artifactId":"a","version":"LATEST"}]}`,
		`{"mode":"dependency-set","coordinates":[{"groupId":"g","artifactId":"a","version":"[1.0,2.0]"}]}`,
	}
	for _, raw := range bad {
		src := base
		src.ConfigJSON = json.RawMessage(raw)
		if err := a.ValidateConfig(context.Background(), src); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("config %s should be invalid, got %v", raw, err)
		}
	}
	ok := base
	ok.ConfigJSON = json.RawMessage(`{"mode":"dependency-set","coordinates":[{"groupId":"org.example","artifactId":"lib","version":"1.2.3"}]}`)
	if err := a.ValidateConfig(context.Background(), ok); err != nil {
		t.Fatal(err)
	}
}

func TestGenericInventoryRequiresFilesystemMode(t *testing.T) {
	a := NewGeneric(nil)
	src := domain.Source{
		Type: domain.SourceMaven, Provider: GenericProvider,
		UpstreamURL: "https://upstream.example/maven", RootPath: t.TempDir(),
		ConfigJSON: json.RawMessage(`{"indexUrl":"https://upstream.example/index.ndjson"}`),
	}
	_, err := a.Inventory(context.Background(), ports.InventoryRequest{Source: src}, nil)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing inventoryMode=filesystem should fail, got %v", err)
	}
}
