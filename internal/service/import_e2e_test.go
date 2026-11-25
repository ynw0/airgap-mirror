package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ynw0/airgap-mirror/internal/adapters/apt"
	"github.com/ynw0/airgap-mirror/internal/adapters/pypi"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
	storesqlite "github.com/ynw0/airgap-mirror/internal/storage/sqlite"
)

func TestPackImportPublishEpochComplete(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	staging := filepath.Join(t.TempDir(), "staging")
	if err := os.MkdirAll(staging, 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(8)
	if err = storesqlite.InitServer(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := storesqlite.NewServerStore(db)

	now := time.Now().UTC()
	deb := []byte("fake-deb")
	pkgIndex := []byte(fmt.Sprintf("Package: demo\nVersion: 1\nFilename: pool/main/a.deb\nSize: %d\nSHA256: %s\n\n", len(deb), shaHex(string(deb))))
	release := []byte(fmt.Sprintf("Suite: bookworm\nDate: %s\nSHA256:\n %s %d main/binary-amd64/Packages\n", now.Format(time.RFC1123), shaHex(string(pkgIndex)), len(pkgIndex)))
	keyring, inrelease := signedImportFixture(t, release)
	src := domain.Source{
		ID: mustE2EID(t), Name: "debian", Type: domain.SourceAPT, Provider: apt.Provider,
		UpstreamURL: "https://deb.debian.org/debian", PublicURL: "https://repo.internal/apt",
		RootPath: root, Enabled: true,
		ConfigJSON: json.RawMessage(fmt.Sprintf(`{"suite":"bookworm","releaseMode":"inrelease","trustedKeyringBase64":%q}`, keyring)),
		CreatedAt:  now, UpdatedAt: now,
	}
	if err = store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}

	epochID, batchID, packID := mustE2EID(t), mustE2EID(t), mustE2EID(t)
	unitID, artID, metaID := mustE2EID(t), mustE2EID(t), mustE2EID(t)
	base := domain.Cursor{}
	target := domain.Cursor{Kind: "apt-release-snapshot-sha256", Value: shaHex(string(inrelease))}

	packPath := filepath.Join(staging, "000001.agp")
	w, err := pack.Create(packPath, domain.PackHeader{PackID: packID, EpochID: epochID, BatchID: batchID})
	if err != nil {
		t.Fatal(err)
	}
	art := domain.Artifact{
		ID: artID, EpochID: epochID, SourceID: src.ID, PublishUnitID: unitID,
		LogicalPath: "pool/main/a.deb", Size: int64(len(deb)), SHA256: shaHex(string(deb)),
		Operation: domain.ArtifactAdd,
	}
	artLoc, err := w.Append(ctx, domain.PackEntry{Artifact: art}, bytes.NewReader(deb))
	if err != nil {
		t.Fatal(err)
	}
	meta := domain.Artifact{
		ID: metaID, EpochID: epochID, SourceID: src.ID, PublishUnitID: unitID,
		LogicalPath: "dists/bookworm/InRelease", Size: int64(len(inrelease)), SHA256: shaHex(string(inrelease)),
		Operation: domain.ArtifactAdd, Metadata: true,
	}
	metaLoc, err := w.Append(ctx, domain.PackEntry{Artifact: meta}, bytes.NewReader(inrelease))
	if err != nil {
		t.Fatal(err)
	}
	index := domain.Artifact{
		ID: mustE2EID(t), EpochID: epochID, SourceID: src.ID, PublishUnitID: unitID,
		LogicalPath: "dists/bookworm/main/binary-amd64/Packages", Size: int64(len(pkgIndex)), SHA256: shaHex(string(pkgIndex)),
		Operation: domain.ArtifactAdd, Metadata: true,
	}
	indexLoc, err := w.Append(ctx, domain.PackEntry{Artifact: index}, bytes.NewReader(pkgIndex))
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	packSHA, packSize, err := pack.FileSHA256(packPath)
	if err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(staging, "manifest.sqlite")
	mdb, err := sql.Open("sqlite", manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = pack.InitManifest(ctx, mdb); err != nil {
		t.Fatal(err)
	}
	if err = pack.PutManifestMeta(ctx, mdb, "schema_version", "1"); err != nil {
		t.Fatal(err)
	}
	if err = pack.PutManifestMeta(ctx, mdb, "source_id", src.ID); err != nil {
		t.Fatal(err)
	}
	if err = pack.PutManifestMeta(ctx, mdb, "epoch_id", epochID); err != nil {
		t.Fatal(err)
	}
	if err = pack.PutManifestMeta(ctx, mdb, "batch_id", batchID); err != nil {
		t.Fatal(err)
	}
	unit := domain.PublishUnit{ID: unitID, EpochID: epochID, SourceID: src.ID, Key: "bookworm", Required: 3, MetadataPath: "dists/bookworm/InRelease"}
	if err = pack.PutPublishUnit(ctx, mdb, unit); err != nil {
		t.Fatal(err)
	}
	if err = pack.PutManifestEntry(ctx, mdb, domain.ManifestEntry{EntryID: artID, Artifact: art, PackID: packID, PackOffset: artLoc.Offset, RecordLength: artLoc.RecordLength}); err != nil {
		t.Fatal(err)
	}
	if err = pack.PutManifestEntry(ctx, mdb, domain.ManifestEntry{EntryID: metaID, Artifact: meta, PackID: packID, PackOffset: metaLoc.Offset, RecordLength: metaLoc.RecordLength}); err != nil {
		t.Fatal(err)
	}
	if err = pack.PutManifestEntry(ctx, mdb, domain.ManifestEntry{EntryID: index.ID, Artifact: index, PackID: packID, PackOffset: indexLoc.Offset, RecordLength: indexLoc.RecordLength}); err != nil {
		t.Fatal(err)
	}
	if err = mdb.Close(); err != nil {
		t.Fatal(err)
	}
	manifestSHA, manifestSize, err := pack.FileSHA256(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	desc := domain.BatchDescriptor{
		SchemaVersion: 1, SourceID: src.ID, EpochID: epochID, BatchID: batchID,
		BatchSequence: 1, EpochTotalBytes: int64(len(deb) + len(inrelease) + len(pkgIndex)), EpochTotalObjects: 3,
		EpochTotalBatches: 1, EpochPublishUnitCount: 1, BatchBytes: packSize, BatchObjects: 3,
		BaseCursor: base, TargetCursor: target, ManifestSHA256: manifestSHA, ManifestSize: manifestSize,
		Packs:     []domain.PackSpec{{ID: packID, Sequence: 1, Size: packSize, SHA256: packSHA, EntryCount: 3}},
		CreatedAt: now,
	}
	session := domain.ImportSession{
		ID: mustE2EID(t), RequestID: "batch-" + batchID, SourceID: src.ID, EpochID: epochID, BatchID: batchID,
		ManifestPath: manifestPath, ManifestSize: manifestSize, ManifestSHA256: manifestSHA,
		ManifestUploaded: manifestSize, Status: "OPEN", StagingPath: staging, CreatedAt: now, UpdatedAt: now,
	}
	ip := domain.ImportPack{SessionID: session.ID, PackID: packID, ExpectedSize: packSize, ExpectedSHA256: packSHA, StagingPath: packPath, Status: domain.PackUploading}
	if _, err = store.RegisterImportBundle(ctx, desc, session, []domain.ImportPack{ip}); err != nil {
		t.Fatal(err)
	}
	if err = store.RegisterManifest(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	ip.UploadedSize = packSize
	ip.Status = domain.PackUploaded
	if err = store.UpdateImportPack(ctx, ip); err != nil {
		t.Fatal(err)
	}

	reg, err := NewRegistry(apt.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	importer := ImportService{
		Store: store, Installer: FileInstaller{},
		Manifest:  ManifestReader{Open: func(path string) (*sql.DB, error) { return sql.Open("sqlite", path) }},
		Publisher: PublisherService{Store: store, Catalog: store, Registry: reg},
	}
	if err = importer.CommitPack(ctx, session.ID, packID); err != nil {
		t.Fatal(err)
	}
	if err = importer.CommitPack(ctx, session.ID, packID); err != nil {
		t.Fatal(err)
	}
	if err = importer.CompleteBatch(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if err = importer.CompleteBatch(ctx, session.ID); err != nil {
		t.Fatal(err)
	}

	gotDeb, err := os.ReadFile(filepath.Join(root, "pool", "main", "a.deb"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotDeb, deb) {
		t.Fatalf("artifact content mismatch")
	}
	gotRel, err := os.ReadFile(filepath.Join(root, "dists", "bookworm", "InRelease"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotRel, inrelease) {
		t.Fatalf("metadata published mismatch")
	}
	staged := filepath.Join(root, ".airgap-staging", epochID, unitID, "dists", "bookworm", "InRelease")
	if _, err = os.Stat(staged); err == nil {
		t.Fatal("staged metadata should be published away")
	}
	if _, err = os.Stat(filepath.Join(root, ".airgap-staging", epochID)); err == nil {
		t.Fatal("completed epoch staging should be removed")
	}
	ep, err := store.GetEpoch(ctx, epochID)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Status != domain.EpochComplete {
		t.Fatalf("epoch status=%s", ep.Status)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("completed import staging should be removed, stat err=%v", err)
	}
	state, err := store.GetState(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.LiveCursor.Equal(target) || state.ActiveEpochID != "" || state.LiveObjects != 3 {
		t.Fatalf("source state after complete: %+v", state)
	}
}

func signedImportFixture(t *testing.T, body []byte) (string, []byte) {
	t.Helper()
	gpg, err := exec.LookPath("gpg")
	if err != nil {
		t.Skip("GnuPG required")
	}
	dir, err := os.MkdirTemp("", "agp-import-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if gpgconf, e := exec.LookPath("gpgconf"); e == nil {
			_ = exec.Command(gpgconf, "--homedir", dir, "--kill", "gpg-agent").Run()
		}
		_ = os.RemoveAll(dir)
	})
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, gpg, append([]string{"--no-options", "--homedir", dir, "--batch", "--no-tty", "--pinentry-mode", "loopback", "--passphrase", ""}, args...)...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, e := cmd.Output()
		if e != nil {
			t.Fatalf("gpg fixture: %v: %s", e, stderr.String())
		}
		return out
	}
	run("--quick-gen-key", "Airgap import fixture <fixture@example.invalid>", "ed25519", "sign", "1d")
	keys := run("--export")
	file := filepath.Join(dir, "Release")
	if err = os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(keys), run("--output", "-", "--clearsign", file)
}

func mustE2EID(t *testing.T) string {
	t.Helper()
	id, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type zeroImportCapacity struct{}

func (zeroImportCapacity) Inspect(_ context.Context, path string) (domain.Capacity, error) {
	return domain.Capacity{Path: path, TotalBytes: 1, FreeBytes: 0}, nil
}

func TestApprovedPyPIPackRepairEndToEnd(t *testing.T) {
	t.Run("advancing cursor", func(t *testing.T) { testApprovedPyPIPackRepair(t, false) })
	t.Run("unchanged cursor", func(t *testing.T) { testApprovedPyPIPackRepair(t, true) })
}

func testApprovedPyPIPackRepair(t *testing.T, sameCursor bool) {
	ctx := context.Background()
	root := t.TempDir()
	staging := filepath.Join(t.TempDir(), "staging")
	if err := os.MkdirAll(staging, 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(8)
	if err = storesqlite.InitServer(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := storesqlite.NewServerStore(db)

	now := time.Now().UTC()
	src := domain.Source{
		ID: mustE2EID(t), Name: "PyPI", Type: domain.SourcePyPI, Provider: pypi.Provider,
		UpstreamURL: "https://pypi.org/simple", PublicURL: "https://repo.internal/pypi",
		RootPath: root, Enabled: true,
		ConfigJSON: json.RawMessage(`{}`),
		CreatedAt:  now, UpdatedAt: now,
	}
	if err = store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}

	epochID, batchID, packID := mustE2EID(t), mustE2EID(t), mustE2EID(t)
	unitID, artID, metaID := mustE2EID(t), mustE2EID(t), mustE2EID(t)
	deb := []byte("good")
	inrelease := []byte("<html></html>")
	base := domain.Cursor{}
	target := domain.Cursor{Kind: "pypi-serial", Value: "5"}
	if sameCursor {
		base = target
		state, stateErr := store.GetState(ctx, src.ID)
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		state.LiveCursor = base
		if err = store.PutState(ctx, state); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.MkdirAll(filepath.Join(root, "packages"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "packages", "demo.whl"), []byte("evil"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = store.Upsert(ctx, domain.CatalogEntry{SourceID: src.ID, LogicalPath: "packages/demo.whl", PackageKey: "demo", Size: 4, SHA256: shaHex("good"), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	jobID := mustE2EID(t)
	job := domain.MaintenanceJob{ID: jobID, SourceID: src.ID, Kind: domain.MaintenanceRepair, Status: domain.MaintenanceQueued, HashMiBPerSecond: 64, CreatedAt: now}
	if err = store.CreateMaintenanceJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err = store.TransitionMaintenanceJob(ctx, jobID, domain.MaintenanceRunning, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginRepairScan(ctx, jobID, src.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.CommitRepairPage(ctx, jobID, "", "packages/demo.whl", 1, 4, []domain.RepairIssue{{LogicalPath: "packages/demo.whl", PackageKey: "demo", Size: 4, SHA256: shaHex("good"), Kind: "HASH_MISMATCH", ActualSize: 4, ActualSHA256: shaHex("evil")}}); err != nil {
		t.Fatal(err)
	}
	if err = store.FinishRepairScan(ctx, jobID, now); err != nil {
		t.Fatal(err)
	}
	if err = store.ApproveRepairReplacement(ctx, jobID, true); err != nil {
		t.Fatal(err)
	}

	packPath := filepath.Join(staging, "000001.agp")
	w, err := pack.Create(packPath, domain.PackHeader{PackID: packID, EpochID: epochID, BatchID: batchID})
	if err != nil {
		t.Fatal(err)
	}
	art := domain.Artifact{
		ID: artID, EpochID: epochID, SourceID: src.ID, PublishUnitID: unitID,
		LogicalPath: "packages/demo.whl", Size: int64(len(deb)), SHA256: shaHex(string(deb)),
		Operation: domain.ArtifactAdd, PackageKey: "demo",
	}
	artLoc, err := w.Append(ctx, domain.PackEntry{Artifact: art}, bytes.NewReader(deb))
	if err != nil {
		t.Fatal(err)
	}
	meta := domain.Artifact{
		ID: metaID, EpochID: epochID, SourceID: src.ID, PublishUnitID: unitID,
		LogicalPath: "simple/demo/index.html", Size: int64(len(inrelease)), SHA256: shaHex(string(inrelease)),
		Operation: domain.ArtifactAdd, Metadata: true,
	}
	metaLoc, err := w.Append(ctx, domain.PackEntry{Artifact: meta}, bytes.NewReader(inrelease))
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	packSHA, packSize, err := pack.FileSHA256(packPath)
	if err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(staging, "manifest.sqlite")
	mdb, err := sql.Open("sqlite", manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = pack.InitManifest(ctx, mdb); err != nil {
		t.Fatal(err)
	}
	if err = pack.PutManifestMeta(ctx, mdb, "schema_version", "1"); err != nil {
		t.Fatal(err)
	}
	if err = pack.PutManifestMeta(ctx, mdb, "source_id", src.ID); err != nil {
		t.Fatal(err)
	}
	if err = pack.PutManifestMeta(ctx, mdb, "epoch_id", epochID); err != nil {
		t.Fatal(err)
	}
	if err = pack.PutManifestMeta(ctx, mdb, "batch_id", batchID); err != nil {
		t.Fatal(err)
	}
	unit := domain.PublishUnit{ID: unitID, EpochID: epochID, SourceID: src.ID, Key: "pypi:demo", Required: 2, MetadataPath: "simple/demo/index.html"}
	if err = pack.PutPublishUnit(ctx, mdb, unit); err != nil {
		t.Fatal(err)
	}
	if err = pack.PutManifestEntry(ctx, mdb, domain.ManifestEntry{EntryID: artID, Artifact: art, PackID: packID, PackOffset: artLoc.Offset, RecordLength: artLoc.RecordLength}); err != nil {
		t.Fatal(err)
	}
	if err = pack.PutManifestEntry(ctx, mdb, domain.ManifestEntry{EntryID: metaID, Artifact: meta, PackID: packID, PackOffset: metaLoc.Offset, RecordLength: metaLoc.RecordLength}); err != nil {
		t.Fatal(err)
	}
	if err = mdb.Close(); err != nil {
		t.Fatal(err)
	}
	manifestSHA, manifestSize, err := pack.FileSHA256(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	desc := domain.BatchDescriptor{
		SchemaVersion: 1, SourceID: src.ID, EpochID: epochID, BatchID: batchID,
		BatchSequence: 1, EpochTotalBytes: int64(len(deb) + len(inrelease)), EpochTotalObjects: 2,
		EpochTotalBatches: 1, EpochPublishUnitCount: 1, BatchBytes: packSize, BatchObjects: 2,
		BaseCursor: base, TargetCursor: target, ManifestSHA256: manifestSHA, ManifestSize: manifestSize,
		Packs:     []domain.PackSpec{{ID: packID, Sequence: 1, File: "packs/000001.agp", Size: packSize, SHA256: packSHA, EntryCount: 2}},
		CreatedAt: now,
	}
	session := domain.ImportSession{
		ID: mustE2EID(t), RequestID: "batch-" + batchID, SourceID: src.ID, EpochID: epochID, BatchID: batchID,
		ManifestPath: manifestPath, ManifestSize: manifestSize, ManifestSHA256: manifestSHA,
		ManifestUploaded: manifestSize, Status: "OPEN", StagingPath: staging, CreatedAt: now, UpdatedAt: now,
	}
	ip := domain.ImportPack{SessionID: session.ID, PackID: packID, ExpectedSize: packSize, ExpectedSHA256: packSHA, StagingPath: packPath, Status: domain.PackUploading}
	registrar := BundleRegistrar{Store: store, Capacity: zeroImportCapacity{}, StagingRoot: t.TempDir()}
	if _, err = registrar.Register(ctx, session.RequestID, desc); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("new import must fail zero-space preflight: %v", err)
	}
	if _, err = store.RegisterImportBundle(ctx, desc, session, []domain.ImportPack{ip}); err != nil {
		t.Fatal(err)
	}
	resumed, err := registrar.Register(ctx, session.RequestID, desc)
	if err != nil || resumed.ID != session.ID {
		t.Fatalf("existing import must be resumable despite full-batch space estimate: %+v, %v", resumed, err)
	}
	if _, err = registrar.Register(ctx, "another-request", desc); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("same batch must reject a second request ID: %v", err)
	}
	duplicate := session
	duplicate.ID = mustE2EID(t)
	duplicate.RequestID = "another-request"
	duplicatePack := ip
	duplicatePack.SessionID = duplicate.ID
	if _, err = store.RegisterImportBundle(ctx, desc, duplicate, []domain.ImportPack{duplicatePack}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("database registration must also reject a second request ID: %v", err)
	}
	changed := desc
	changed.Packs = append([]domain.PackSpec(nil), desc.Packs...)
	changed.Packs[0].SHA256 = shaHex("different")
	if _, err = registrar.Register(ctx, session.RequestID, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changed pack must not resume: %v", err)
	}
	sessions, err := store.ListImportSessionsByEpoch(ctx, epochID)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("duplicate registration left another session: %d, %v", len(sessions), err)
	}
	if err = store.RegisterManifest(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	ip.UploadedSize = packSize
	ip.Status = domain.PackUploaded
	if err = store.UpdateImportPack(ctx, ip); err != nil {
		t.Fatal(err)
	}

	reg, err := NewRegistry(pypi.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	importer := ImportService{
		Store: store, Installer: FileInstaller{},
		Manifest:  ManifestReader{Open: func(path string) (*sql.DB, error) { return sql.Open("sqlite", path) }},
		Publisher: PublisherService{Store: store, Catalog: store, Registry: reg},
	}
	if err = importer.CommitPack(ctx, session.ID, packID); err != nil {
		t.Fatal(err)
	}
	if err = importer.CommitPack(ctx, session.ID, packID); err != nil {
		t.Fatal(err)
	}
	if err = importer.CompleteBatch(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if err = importer.CompleteBatch(ctx, session.ID); err != nil {
		t.Fatal(err)
	}

	gotDeb, err := os.ReadFile(filepath.Join(root, "packages", "demo.whl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotDeb, deb) {
		t.Fatalf("artifact content mismatch")
	}
	backups, err := filepath.Glob(filepath.Join(root, ".airgap-staging", "repair-quarantine", jobID, "*", "packages", "demo.whl"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("quarantine missing after complete: %v %v", backups, err)
	}
	original, err := os.ReadFile(backups[0])
	if err != nil || string(original) != "evil" {
		t.Fatalf("corrupt original not preserved: %q %v", original, err)
	}
	report, err := store.GetMaintenanceJob(ctx, jobID)
	if err != nil || report.ReplacementApproved {
		t.Fatalf("completed epoch did not expire approval: %+v %v", report, err)
	}

	gotRel, err := os.ReadFile(filepath.Join(root, "simple", "demo", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotRel, inrelease) {
		t.Fatalf("metadata published mismatch")
	}
	staged := filepath.Join(root, ".airgap-staging", epochID, unitID, "simple", "demo", "index.html")
	if _, err = os.Stat(staged); err == nil {
		t.Fatal("staged metadata should be published away")
	}
	if _, err = os.Stat(filepath.Join(root, ".airgap-staging", epochID)); err == nil {
		t.Fatal("completed epoch staging should be removed")
	}
	ep, err := store.GetEpoch(ctx, epochID)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Status != domain.EpochComplete {
		t.Fatalf("epoch status=%s", ep.Status)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("completed import staging should be removed, stat err=%v", err)
	}
	state, err := store.GetState(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.LiveCursor.Equal(target) || state.ActiveEpochID != "" || state.LiveObjects != 2 {
		t.Fatalf("source state after complete: %+v", state)
	}
	retry, err := registrar.Register(ctx, session.RequestID, desc)
	if err != nil || retry.ID != session.ID || retry.Status != "BATCH_IMPORTED" {
		t.Fatalf("completed import retry: %+v, %v", retry, err)
	}
	if err = importer.CompleteBatch(ctx, retry.ID); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetState(ctx, src.ID)
	if err != nil || after.CatalogVersion != state.CatalogVersion || after.ActiveEpochID != "" || !after.LiveCursor.Equal(target) {
		t.Fatalf("completed retry changed source state: %+v, %v", after, err)
	}
}
