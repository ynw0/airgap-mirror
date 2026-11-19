package apt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
	storesqlite "github.com/ynw0/airgap-mirror/internal/storage/sqlite"
)

type absentCatalog struct{}

func (absentCatalog) Get(context.Context, string, string) (domain.CatalogEntry, error) {
	return domain.CatalogEntry{}, domain.ErrNotFound
}
func (absentCatalog) ListByPackage(context.Context, string, string) ([]domain.CatalogEntry, error) {
	return nil, nil
}
func (absentCatalog) Upsert(context.Context, domain.CatalogEntry) error { return nil }
func (absentCatalog) Delete(context.Context, string, string) error { return nil }
func (absentCatalog) Stats(context.Context, string) (domain.CatalogStats, error) {
	return domain.CatalogStats{}, nil
}

func testDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func TestAnalyzeDeclaresImmutablePublishUnitOnce(t *testing.T) {
	ctx := context.Background()
	pkg := []byte(fmt.Sprintf("Package: demo\nVersion: 1\nFilename: pool/main/d/demo.deb\nSize: 3\nSHA256: %s\n\n", testDigest([]byte("deb"))))
	release := []byte(fmt.Sprintf("SHA256:\n %s %d main/binary-amd64/Packages\n", testDigest(pkg), len(pkg)))
	mux := http.NewServeMux()
	mux.HandleFunc("/dists/stable/Release", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(release) })
	mux.HandleFunc("/dists/stable/Release.gpg", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("sig")) })
	mux.HandleFunc("/dists/stable/main/binary-amd64/Packages", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(pkg) })
	server := httptest.NewServer(mux)
	defer server.Close()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "client.db"))
	if err != nil { t.Fatal(err) }
	defer db.Close()
	if err := storesqlite.InitClient(ctx, db); err != nil { t.Fatal(err) }
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO state_capsules(id,file_path,exported_at,imported_at,state_json) VALUES(?,?,?,?,?)`, "snapshot", "", now, now, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	store := storesqlite.NewClientStore(db)
	src := domain.Source{ID: "source", Type: domain.SourceAPT, Provider: Provider, UpstreamURL: server.URL, PublicURL: "https://repo.example/apt", ConfigJSON: json.RawMessage(`{"suite":"stable","releaseMode":"detached"}`)}
	plan, err := New(server.Client()).Analyze(ctx, ports.AnalyzeRequest{
		CapsuleID: "snapshot", Source: src, Workset: store, Catalog: absentCatalog{},
	}, store)
	if err != nil { t.Fatal(err) }
	if plan.ArtifactCount != 4 || plan.MetadataCount != 3 || plan.PublishUnitCount != 1 {
		t.Fatalf("unexpected APT plan: %+v", plan)
	}
	var required int64
	if err := db.QueryRowContext(ctx, `SELECT required_count FROM planned_publish_units WHERE epoch_id=?`, plan.Epoch.ID).Scan(&required); err != nil {
		t.Fatal(err)
	}
	if required != 4 { t.Fatalf("required_count=%d, want 4", required) }
}
