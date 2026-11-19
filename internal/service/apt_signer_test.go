package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ynw0/airgap-mirror/internal/adapters/apt"
	"github.com/ynw0/airgap-mirror/internal/domain"
	storesqlite "github.com/ynw0/airgap-mirror/internal/storage/sqlite"
)

func TestAPTSigningRequiresCompletedInventory(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = storesqlite.InitServer(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := storesqlite.NewServerStore(db)
	sourceID, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	src := domain.Source{
		ID: sourceID, Name: "debian", Type: domain.SourceAPT, Provider: apt.Provider,
		UpstreamURL: "https://deb.debian.org/debian", PublicURL: "http://repo.local/debian",
		RootPath: t.TempDir(), Enabled: true, ConfigJSON: json.RawMessage(`{"suite":"bookworm","releaseMode":"inrelease"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if err = store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	signer := &APTSigner{Sources: store, Catalog: store, Jobs: store, GNUPGHOME: t.TempDir(), KeyID: strings.Repeat("a", 40)}
	if _, err = signer.Sign(ctx, sourceID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("should require a completed inventory: %v", err)
	}
}
