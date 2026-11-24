package pypi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
	storesqlite "github.com/ynw0/airgap-mirror/internal/storage/sqlite"
)

type absentProjectCatalog struct{}

func (absentProjectCatalog) Get(context.Context, string, string) (domain.CatalogEntry, error) {
	return domain.CatalogEntry{}, domain.ErrNotFound
}
func (absentProjectCatalog) ListByPackage(context.Context, string, string) ([]domain.CatalogEntry, error) {
	return nil, nil
}
func (absentProjectCatalog) Upsert(context.Context, domain.CatalogEntry) error {
	return nil
}
func (absentProjectCatalog) Delete(context.Context, string, string) error {
	return nil
}
func (absentProjectCatalog) Stats(context.Context, string) (domain.CatalogStats, error) {
	return domain.CatalogStats{}, nil
}

type testGenerated struct{ root string }

func (g testGenerated) Put(_ context.Context, _, logical string, content []byte) (string, string, int64, error) {
	path := filepath.Join(g.root, filepath.FromSlash(logical))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", "", 0, err
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		return "", "", 0, err
	}
	sum := sha256.Sum256(content)
	return path, hex.EncodeToString(sum[:]), int64(len(content)), nil
}

func TestAnalyzeDeclaresImmutablePublishUnitOnce(t *testing.T) {
	ctx := context.Background()
	wheel := []byte("wheel")
	sum := sha256.Sum256(wheel)
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/simple/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-PyPI-Last-Serial", "5")
		_, _ = w.Write([]byte(`{"meta":{"api-version":"1.0","_last-serial":5},"projects":[{"name":"demo","_last-serial":5}]}`))
	})
	mux.HandleFunc("/simple/demo/", func(w http.ResponseWriter, r *http.Request) {
		body := fmt.Sprintf(`{"meta":{"api-version":"1.0"},"name":"demo","files":[{"filename":"demo.whl","url":"%s/packages/d/demo.whl","hashes":{"sha256":"%s"},"size":5}]}`, serverURL, hex.EncodeToString(sum[:]))
		_, _ = w.Write([]byte(body))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "client.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storesqlite.InitClient(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO state_capsules(id,file_path,exported_at,imported_at,state_json) VALUES(?,?,?,?,?)`, "snapshot", "", now, now, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	store := storesqlite.NewClientStore(db)
	src := domain.Source{ID: "source", Type: domain.SourcePyPI, Provider: Provider, UpstreamURL: server.URL + "/simple", PublicURL: "https://repo.example/pypi", ConfigJSON: json.RawMessage(`{}`)}
	plan, err := New(server.Client()).Analyze(ctx, ports.AnalyzeRequest{
		CapsuleID: "snapshot", Source: src, Workset: store,
		Catalog: absentProjectCatalog{}, Generated: testGenerated{root: t.TempDir()},
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ArtifactCount != 2 || plan.MetadataCount != 1 || plan.PublishUnitCount != 1 {
		t.Fatalf("unexpected PyPI plan: %+v", plan)
	}
	var required int64
	if err := db.QueryRowContext(ctx, `SELECT required_count FROM planned_publish_units WHERE epoch_id=?`, plan.Epoch.ID).Scan(&required); err != nil {
		t.Fatal(err)
	}
	if required != 2 {
		t.Fatalf("required_count=%d, want 2", required)
	}
}

type repairProjectCatalog struct {
	absentProjectCatalog
	entry domain.CatalogEntry
}

func (c repairProjectCatalog) WalkRepairProjects(ctx context.Context, sourceID string, visit func(string) error) error {
	return visit(c.entry.PackageKey)
}

func (c repairProjectCatalog) RepairEntries(ctx context.Context, sourceID, project string) ([]domain.CatalogEntry, error) {
	if project == c.entry.PackageKey {
		return []domain.CatalogEntry{c.entry}, nil
	}
	return nil, nil
}

func TestRepairAnalyzesProjectEvenWhenSerialHasNotChanged(t *testing.T) {
	ctx := context.Background()
	sum := sha256.Sum256([]byte("wheel"))
	hash := hex.EncodeToString(sum[:])
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/simple/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-PyPI-Last-Serial", "5")
		_, _ = w.Write([]byte(`{"meta":{"api-version":"1.0","_last-serial":5},"projects":[{"name":"demo","_last-serial":5}]}`))
	})
	mux.HandleFunc("/simple/demo/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"meta":{"api-version":"1.0"},"name":"demo","files":[{"filename":"demo.whl","url":"%s/packages/d/demo.whl","hashes":{"sha256":"%s"},"size":5}]}`, serverURL, hash)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "client.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = storesqlite.InitClient(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = db.ExecContext(ctx, `INSERT INTO state_capsules(id,file_path,exported_at,imported_at,state_json) VALUES(?,?,?,?,?)`, "snapshot", "", now, now, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	store := storesqlite.NewClientStore(db)
	src := domain.Source{ID: "source", Type: domain.SourcePyPI, Provider: Provider, UpstreamURL: server.URL + "/simple", PublicURL: "https://repo.example/pypi", ConfigJSON: json.RawMessage("{}")}
	base := domain.Cursor{Kind: "pypi-serial", Value: "5"}
	entry := domain.CatalogEntry{SourceID: src.ID, LogicalPath: "packages/d/demo.whl", PackageKey: "demo", Size: 5, SHA256: hash}
	plan, err := New(server.Client()).Analyze(ctx, ports.AnalyzeRequest{CapsuleID: "snapshot", Source: src, Base: base, Workset: store, Catalog: repairProjectCatalog{entry: entry}, Generated: testGenerated{root: t.TempDir()}}, store)
	if err != nil {
		t.Fatal(err)
	}
	if plan.PublishUnitCount != 1 || plan.ArtifactCount != 2 || !plan.Epoch.TargetCursor.Equal(base) {
		t.Fatalf("missing file was not planned independently of cursor: %+v", plan)
	}
}
