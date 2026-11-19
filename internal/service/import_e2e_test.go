package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ynw0/airgap-mirror/internal/adapters/apt"
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
	src := domain.Source{
		ID: mustE2EID(t), Name: "debian", Type: domain.SourceAPT, Provider: apt.Provider,
		UpstreamURL: "https://deb.debian.org/debian", PublicURL: "https://repo.internal/apt",
		RootPath: root, Enabled: true,
		ConfigJSON: json.RawMessage(`{"suite":"bookworm","releaseMode":"inrelease"}`),
		CreatedAt:  now, UpdatedAt: now,
	}
	if err = store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}

	epochID, batchID, packID := mustE2EID(t), mustE2EID(t), mustE2EID(t)
	unitID, artID, metaID := mustE2EID(t), mustE2EID(t), mustE2EID(t)
	deb := []byte("fake-deb")
	inrelease := []byte("InRelease-body")
	base := domain.Cursor{}
	target := domain.Cursor{Kind: "apt-release", Value: "frozen-1"}

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
	unit := domain.PublishUnit{ID: unitID, EpochID: epochID, SourceID: src.ID, Key: "bookworm", Required: 2, MetadataPath: "dists/bookworm/InRelease"}
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
		Packs:     []domain.PackSpec{{ID: packID, Sequence: 1, Size: packSize, SHA256: packSHA, EntryCount: 2}},
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
	if !state.LiveCursor.Equal(target) || state.ActiveEpochID != "" || state.LiveObjects != 2 {
		t.Fatalf("source state after complete: %+v", state)
	}
}

func mustE2EID(t *testing.T) string {
	t.Helper()
	id, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
