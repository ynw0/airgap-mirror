package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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

func TestAPTReSignAfterInventoryProducesVerifiableMetadata(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("GPG is not installed")
	}
	ctx := context.Background()
	home := t.TempDir()
	gpg := func(args ...string) string {
		t.Helper()
		base := []string{"--homedir", home, "--batch", "--no-tty"}
		cmd := exec.CommandContext(ctx, "gpg", append(base, args...)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("gpg %v: %v: %s", args, err, output)
		}
		return string(output)
	}
	gpg("--pinentry-mode", "loopback", "--passphrase", "", "--quick-generate-key", "Local Test <test@local.invalid>", "ed25519", "sign", "1d")
	var fingerprint string
	for _, line := range strings.Split(gpg("--with-colons", "--list-secret-keys"), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > 9 && fields[0] == "fpr" {
			fingerprint = fields[9]
			break
		}
	}
	if fingerprint == "" {
		t.Fatal("GPG returned no signing fingerprint")
	}

	root := t.TempDir()
	dist := filepath.Join(root, "dists", "bookworm")
	packages := filepath.Join(dist, "main", "binary-amd64", "Packages")
	if err := os.MkdirAll(filepath.Dir(packages), 0755); err != nil {
		t.Fatal(err)
	}
	content := []byte("test")
	if err := os.WriteFile(packages, content, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	release := []byte("Origin: Local\nSHA256:\n " + hex.EncodeToString(sum[:]) + " 4 main/binary-amd64/Packages\n")
	releasePath := filepath.Join(t.TempDir(), "Release")
	if err := os.WriteFile(releasePath, release, 0600); err != nil {
		t.Fatal(err)
	}
	inRelease := filepath.Join(dist, "InRelease")
	gpg("--yes", "--pinentry-mode", "loopback", "--local-user", fingerprint, "--armor", "--clearsign", "--output", inRelease, releasePath)
	body, err := os.ReadFile(inRelease)
	if err != nil {
		t.Fatal(err)
	}
	inSum := sha256.Sum256(body)

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
	id, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	src := domain.Source{
		ID: id, Name: "local", Type: domain.SourceAPT, Provider: apt.Provider,
		RootPath: root, UpstreamURL: "https://deb.example/repo",
		PublicURL: "http://repo.local/apt", Enabled: true,
		ConfigJSON: json.RawMessage(`{"suite":"bookworm","releaseMode":"inrelease"}`),
		CreatedAt:  now, UpdatedAt: now,
	}
	if err = store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []domain.CatalogEntry{
		{SourceID: id, LogicalPath: "dists/bookworm/InRelease", SHA256: hex.EncodeToString(inSum[:]), Size: int64(len(body)), UpdatedAt: now},
		{SourceID: id, LogicalPath: "dists/bookworm/main/binary-amd64/Packages", SHA256: hex.EncodeToString(sum[:]), Size: 4, UpdatedAt: now},
	} {
		if err = store.Upsert(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	state, err := store.GetState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	state.LiveObjects = 2
	state.LiveBytes = int64(len(body)) + 4
	if err = store.PutState(ctx, state); err != nil {
		t.Fatal(err)
	}
	jobID, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateMaintenanceJob(ctx, domain.MaintenanceJob{
		ID: jobID, SourceID: id, Kind: domain.MaintenanceInventory,
		Status: domain.MaintenanceQueued, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err = store.TransitionMaintenanceJob(ctx, jobID, domain.MaintenanceRunning, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE maintenance_jobs SET scanned_objects=?,scanned_bytes=? WHERE id=?`, 2, state.LiveBytes, jobID); err != nil {
		t.Fatal(err)
	}
	if err = store.TransitionMaintenanceJob(ctx, jobID, domain.MaintenanceCompleted, "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	signer := &APTSigner{Sources: store, Catalog: store, Jobs: store, GNUPGHOME: home, KeyID: fingerprint}
	result, err := signer.Sign(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if result.Suite != "bookworm" || len(result.Files) != 3 {
		t.Fatalf("unexpected sign result: %+v", result)
	}
	gpg("--verify", filepath.Join(dist, "InRelease"))
	gpg("--verify", filepath.Join(dist, "Release.gpg"), filepath.Join(dist, "Release"))
	pub, err := signer.ExportPublicKey(ctx)
	if err != nil || !strings.Contains(string(pub), "BEGIN PGP PUBLIC KEY BLOCK") {
		t.Fatalf("public key export: %v", err)
	}
	newState, err := store.GetState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if newState.LiveObjects != 4 || newState.LiveBytes <= state.LiveBytes {
		t.Fatalf("signing did not advance catalog summary: %+v", newState)
	}
}
