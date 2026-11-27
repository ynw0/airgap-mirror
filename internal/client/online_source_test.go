package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/service"
)

func TestOnlineSourceDoesNotChangeExportedRepository(t *testing.T) {
	original := domain.Source{
		ID: "debian", Type: domain.SourceAPT, Provider: "apt-generic",
		UpstreamURL: "http://mirror.costar.com/apt/debin",
		PublicURL:   "http://mirror.costar.com/apt/debin",
		RootPath:    "/srv/mirror/debian",
		ConfigJSON:  json.RawMessage(`{"suite":"bookworm-updates","releaseMode":"inrelease"}`),
	}
	actual, err := onlineSource(original, "https://deb.debian.org/debian", "")
	if err != nil {
		t.Fatal(err)
	}
	if actual.UpstreamURL != "https://deb.debian.org/debian" || actual.ID != original.ID ||
		actual.PublicURL != original.PublicURL || actual.RootPath != original.RootPath ||
		string(actual.ConfigJSON) != string(original.ConfigJSON) {
		t.Fatalf("unexpected change to repository identity: %+v", actual)
	}
	if original.UpstreamURL != original.PublicURL {
		t.Fatal("exported source changed")
	}
}

func TestOnlineSourceProbeUsesSelectedAddress(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/simple/" {
			t.Errorf("wrong upstream path: %s", r.URL.Path)
		}
		w.Header().Set("X-PyPI-Last-Serial", "123")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	source := domain.Source{Type: domain.SourcePyPI, Provider: "pypi", PublicURL: "http://mirror.costar.com/pypi", UpstreamURL: "http://mirror.costar.com/pypi"}
	selected, err := onlineSource(source, server.URL+"/simple", "")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := service.NewDefaultRegistry(server.Client())
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := registry.Get(selected.Type, selected.Provider)
	if err != nil {
		t.Fatal(err)
	}
	state, err := adapter.Probe(ctx, selected)
	if err != nil || state.Cursor.Value != "123" {
		t.Fatalf("probe did not use selected address: %+v, %v", state, err)
	}
}

func TestOnlineMavenManifestDoesNotChangeSavedConfiguration(t *testing.T) {
	source := domain.Source{Type: domain.SourceMaven, Provider: "maven-generic", ConfigJSON: json.RawMessage(`{"inventoryMode":"filesystem","custom":true}`)}
	selected, err := onlineSource(source, "https://example.org/maven", "https://example.org/index.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(selected.ConfigJSON, &config); err != nil || config["custom"] != true || config["indexUrl"] != "https://example.org/index.ndjson" {
		t.Fatalf("invalid online configuration: %s, %v", selected.ConfigJSON, err)
	}
	if string(source.ConfigJSON) != `{"inventoryMode":"filesystem","custom":true}` {
		t.Fatal("saved config changed")
	}
	if _, err = onlineSource(source, "https://example.org/maven", ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing manifest accepted: %v", err)
	}
	if _, err = onlineSource(source, "file:///tmp/maven", "https://example.org/index.ndjson"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid address accepted: %v", err)
	}
}
