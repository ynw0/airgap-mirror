package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func mustID(t *testing.T) string {
	t.Helper()
	id, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func shaOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func seedImport(t *testing.T, store *ServerStore, required int64) (src domain.Source, epochID, unitID string) {
	t.Helper()
	ctx := context.Background()
	src = testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	epochID = mustID(t)
	now := time.Now().UTC()
	if err := store.CreateEpoch(ctx, domain.Epoch{
		ID: epochID, SourceID: src.ID, Status: domain.EpochTransferring,
		TotalBatches: 2, PublishUnitCount: 1, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	unitID = mustID(t)
	if err := store.DeclarePublishUnit(ctx, domain.PublishUnit{
		ID: unitID, EpochID: epochID, SourceID: src.ID, Key: "pkg", Required: required, Status: domain.PublishWaiting,
	}); err != nil {
		t.Fatal(err)
	}
	return src, epochID, unitID
}

func artifactEntry(entryID, epochID, sourceID, unitID, path, body string, metadata bool) (domain.ManifestEntry, domain.CatalogEntry) {
	sha := shaOf(body)
	a := domain.Artifact{
		ID: entryID, EpochID: epochID, SourceID: sourceID, PublishUnitID: unitID,
		LogicalPath: path, Size: int64(len(body)), SHA256: sha,
		Operation: domain.ArtifactAdd, Metadata: metadata,
	}
	return domain.ManifestEntry{EntryID: entryID, Artifact: a}, domain.CatalogEntry{
		SourceID: sourceID, LogicalPath: path, Size: a.Size, SHA256: sha, UpdatedAt: time.Now().UTC(),
	}
}

func TestCommitImportedEntryIdempotent(t *testing.T) {
	store := openServerDB(t)
	ctx := context.Background()
	src, epochID, unitID := seedImport(t, store, 1)
	entryID := mustID(t)
	m, c := artifactEntry(entryID, epochID, src.ID, unitID, "pool/a.deb", "deb-a", false)
	ok, err := store.CommitImportedEntry(ctx, m, c, "")
	if err != nil || !ok {
		t.Fatalf("first commit ok=%v err=%v", ok, err)
	}
	ok, err = store.CommitImportedEntry(ctx, m, c, "")
	if err != nil || ok {
		t.Fatalf("retry must not re-count ok=%v err=%v", ok, err)
	}
	unit, err := store.GetPublishUnit(ctx, unitID)
	if err != nil {
		t.Fatal(err)
	}
	if unit.Imported != 1 || unit.Status != domain.PublishReady {
		t.Fatalf("imported=%d status=%s", unit.Imported, unit.Status)
	}
}

func TestCommitImportedEntryIdentityMismatch(t *testing.T) {
	store := openServerDB(t)
	ctx := context.Background()
	src, epochID, unitID := seedImport(t, store, 2)
	entryID := mustID(t)
	m, c := artifactEntry(entryID, epochID, src.ID, unitID, "pool/a.deb", "deb-a", false)
	if _, err := store.CommitImportedEntry(ctx, m, c, ""); err != nil {
		t.Fatal(err)
	}
	m.Artifact.SHA256 = shaOf("other")
	if _, err := store.CommitImportedEntry(ctx, m, c, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected identity conflict, got %v", err)
	}
}

func TestPublishUnitReadyAcrossEntries(t *testing.T) {
	store := openServerDB(t)
	ctx := context.Background()
	src, epochID, unitID := seedImport(t, store, 2)
	m1, c1 := artifactEntry(mustID(t), epochID, src.ID, unitID, "pool/a.deb", "deb-a", false)
	m2, c2 := artifactEntry(mustID(t), epochID, src.ID, unitID, "dists/InRelease", "meta", true)
	if _, err := store.CommitImportedEntry(ctx, m1, c1, ""); err != nil {
		t.Fatal(err)
	}
	unit, err := store.GetPublishUnit(ctx, unitID)
	if err != nil {
		t.Fatal(err)
	}
	if unit.Imported != 1 || unit.Status != domain.PublishWaiting {
		t.Fatalf("partial import imported=%d status=%s", unit.Imported, unit.Status)
	}
	if _, err = store.CommitImportedEntry(ctx, m2, c2, "staged/InRelease"); err != nil {
		t.Fatal(err)
	}
	unit, err = store.GetPublishUnit(ctx, unitID)
	if err != nil {
		t.Fatal(err)
	}
	if unit.Imported != 2 || unit.Status != domain.PublishReady {
		t.Fatalf("cross-entry unit imported=%d status=%s", unit.Imported, unit.Status)
	}
	meta, err := store.ListPublishMetadata(ctx, unitID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta) != 1 || meta[0].Operation != domain.ArtifactAdd || meta[0].StagedPath != "staged/InRelease" {
		t.Fatalf("metadata entries=%+v", meta)
	}
}

func TestCatalogHashConflictIsHard(t *testing.T) {
	store := openServerDB(t)
	ctx := context.Background()
	src, epochID, unitID := seedImport(t, store, 2)
	m1, c1 := artifactEntry(mustID(t), epochID, src.ID, unitID, "pool/a.deb", "deb-a", false)
	if _, err := store.CommitImportedEntry(ctx, m1, c1, ""); err != nil {
		t.Fatal(err)
	}
	m2, c2 := artifactEntry(mustID(t), epochID, src.ID, unitID, "pool/a.deb", "deb-B", false)
	if _, err := store.CommitImportedEntry(ctx, m2, c2, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected hard hash conflict, got %v", err)
	}
	got, err := store.Get(ctx, src.ID, "pool/a.deb")
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != c1.SHA256 {
		t.Fatalf("catalog was overwritten: %s", got.SHA256)
	}
}

func TestPublishUnitOverflowRejected(t *testing.T) {
	store := openServerDB(t)
	ctx := context.Background()
	src, epochID, unitID := seedImport(t, store, 1)
	m1, c1 := artifactEntry(mustID(t), epochID, src.ID, unitID, "pool/a.deb", "deb-a", false)
	m2, c2 := artifactEntry(mustID(t), epochID, src.ID, unitID, "pool/b.deb", "deb-b", false)
	if _, err := store.CommitImportedEntry(ctx, m1, c1, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitImportedEntry(ctx, m2, c2, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected overflow conflict, got %v", err)
	}
}

func TestDeclarePublishUnitMismatch(t *testing.T) {
	store := openServerDB(t)
	ctx := context.Background()
	_, epochID, unitID := seedImport(t, store, 2)
	src := testSource(t)
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	err := store.DeclarePublishUnit(ctx, domain.PublishUnit{ID: unitID, EpochID: epochID, SourceID: src.ID, Key: "pkg", Required: 3})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected declaration mismatch, got %v", err)
	}
}
