package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

type planFake struct {
	items    []ports.PlannedArtifact
	assigned map[string]struct{}
	batches  []domain.Batch
	packs    []domain.Pack
	epoch    domain.Epoch
}

func (f *planFake) Begin(context.Context, domain.Epoch, string) error { return nil }
func (f *planFake) PutPublishUnit(context.Context, domain.PublishUnit) error {
	return nil
}
func (f *planFake) PutArtifact(context.Context, domain.Artifact) error { return nil }
func (f *planFake) Commit(context.Context, domain.EpochPlan) error     { return nil }
func (f *planFake) Abort(context.Context, string, error) error        { return nil }
func (f *planFake) SetArtifactPackLocation(context.Context, string, domain.PackLocation) error {
	return nil
}
func (f *planFake) FinalizeArtifact(context.Context, string, int64, string) error { return nil }

func (f *planFake) ListUnassignedArtifacts(_ context.Context, _ string, after int64, limit int) ([]ports.PlannedArtifact, error) {
	var out []ports.PlannedArtifact
	for _, it := range f.items {
		if it.Ordinal <= after {
			continue
		}
		if _, ok := f.assigned[it.Artifact.ID]; ok {
			continue
		}
		out = append(out, it)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *planFake) AssignArtifact(_ context.Context, artifactID, batchID, packID string) error {
	if f.assigned == nil {
		f.assigned = map[string]struct{}{}
	}
	f.assigned[artifactID] = struct{}{}
	for i := range f.items {
		if f.items[i].Artifact.ID == artifactID {
			f.items[i].BatchID = batchID
			f.items[i].PackID = packID
		}
	}
	return nil
}

func (f *planFake) UpdateEpoch(_ context.Context, e domain.Epoch) error { f.epoch = e; return nil }
func (f *planFake) CreateBatch(_ context.Context, b domain.Batch) error {
	f.batches = append(f.batches, b)
	return nil
}
func (f *planFake) UpdateBatch(_ context.Context, b domain.Batch) error {
	for i := range f.batches {
		if f.batches[i].ID == b.ID {
			f.batches[i] = b
			return nil
		}
	}
	return errors.New("unknown batch")
}
func (f *planFake) CreatePack(_ context.Context, p domain.Pack) error {
	f.packs = append(f.packs, p)
	return nil
}
func (f *planFake) UpdatePack(_ context.Context, p domain.Pack) error {
	for i := range f.packs {
		if f.packs[i].ID == p.ID {
			f.packs[i] = p
			return nil
		}
	}
	return errors.New("unknown pack")
}
func (f *planFake) AddDownloadEntry(context.Context, domain.Artifact, string, string) error {
	return nil
}

func artifact(id string, size int64, op domain.ArtifactOperation, metadata bool) domain.Artifact {
	sha := ""
	if op == domain.ArtifactDelete {
		sha = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	} else {
		sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}
	return domain.Artifact{ID: id, LogicalPath: "p/" + id, Size: size, SHA256: sha, Operation: op, Metadata: metadata}
}

func TestPlannerEmptyEpochCreatesOneBatch(t *testing.T) {
	fake := &planFake{}
	p := BatchPlanner{Plans: fake, Transfers: fake, MaxBatchBytes: 1000, TargetPackBytes: 100, PageSize: 10}
	ep, err := p.Plan(context.Background(), domain.Epoch{ID: "e1", SourceID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if ep.TotalBatches != 1 || len(fake.batches) != 1 || len(fake.packs) != 1 {
		t.Fatalf("empty epoch should freeze one batch/pack, got batches=%d packs=%d total=%d", len(fake.batches), len(fake.packs), ep.TotalBatches)
	}
}

func TestPlannerSplitsAcrossPackAndBatch(t *testing.T) {
	fake := &planFake{items: []ports.PlannedArtifact{
		{Ordinal: 1, Artifact: artifact("a1", 1, domain.ArtifactAdd, false)},
		{Ordinal: 2, Artifact: artifact("a2", 1, domain.ArtifactAdd, false)},
		{Ordinal: 3, Artifact: artifact("a3", 1, domain.ArtifactAdd, false)},
	}}
	// record 开销约 84 字节；pack 目标 90 使每个 artifact 独占一个 Pack，batch 200 则前两个同批。
	p := BatchPlanner{Plans: fake, Transfers: fake, MaxBatchBytes: 200, TargetPackBytes: 90, PageSize: 10}
	ep, err := p.Plan(context.Background(), domain.Epoch{ID: "e1", SourceID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if ep.TotalBatches != 2 {
		t.Fatalf("total batches=%d", ep.TotalBatches)
	}
	if len(fake.packs) < 3 {
		t.Fatalf("oversized-per-pack artifacts should not share a pack, packs=%d", len(fake.packs))
	}
	for _, pk := range fake.packs {
		if pk.EntryCount != 1 {
			t.Fatalf("artifact must not span or share undersized pack: %+v", pk)
		}
	}
}

func TestPlannerRejectsPhysicalDeleteInSync(t *testing.T) {
	fake := &planFake{items: []ports.PlannedArtifact{
		{Ordinal: 1, Artifact: artifact("del", 12, domain.ArtifactDelete, false)},
	}}
	p := BatchPlanner{Plans: fake, Transfers: fake, MaxBatchBytes: 1000, TargetPackBytes: 100, PageSize: 10}
	if _, err := p.Plan(context.Background(), domain.Epoch{ID: "e1", SourceID: "s1"}); err == nil {
		t.Fatal("physical artifact deletion in sync pipeline should be rejected")
	}
}

func TestPlannerAllowsMetadataTombstone(t *testing.T) {
	a := artifact("tomb", 0, domain.ArtifactDelete, true)
	fake := &planFake{items: []ports.PlannedArtifact{{Ordinal: 1, Artifact: a}}}
	p := BatchPlanner{Plans: fake, Transfers: fake, MaxBatchBytes: 1000, TargetPackBytes: 100, PageSize: 10}
	ep, err := p.Plan(context.Background(), domain.Epoch{ID: "e1", SourceID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if ep.TotalBatches != 1 {
		t.Fatalf("total batches=%d", ep.TotalBatches)
	}
}
