package service

import (
	"context"
	"fmt"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
	"github.com/ynw0/airgap-mirror/internal/ports"
	"time"
)

type BatchPlanner struct {
	Plans                          ports.PlanStore
	Transfers                      ports.TransferPlanStore
	MaxBatchBytes, TargetPackBytes int64
	PageSize                       int
}

func (p BatchPlanner) Plan(ctx context.Context, e domain.Epoch) (domain.Epoch, error) {
	if p.MaxBatchBytes <= 0 || p.TargetPackBytes <= 0 {
		return e, fmt.Errorf("invalid planner sizes")
	}
	if p.PageSize <= 0 {
		p.PageSize = 1000
	}
	var b *domain.Batch
	var k *domain.Pack
	bs, ps := 0, 0
	var after int64
	fp := func() error {
		if k == nil {
			return nil
		}
		x := p.Transfers.UpdatePack(ctx, *k)
		k = nil
		return x
	}
	fb := func() error {
		if b == nil {
			return nil
		}
		if x := fp(); x != nil {
			return x
		}
		x := p.Transfers.UpdateBatch(ctx, *b)
		b = nil
		return x
	}
	for {
		items, x := p.Plans.ListUnassignedArtifacts(ctx, e.ID, after, p.PageSize)
		if x != nil {
			return e, x
		}
		if len(items) == 0 {
			break
		}
		for _, it := range items {
			after = it.Ordinal
			a := it.Artifact
			if a.Operation == domain.ArtifactDelete && (!a.Metadata || a.Size != 0 || a.SHA256 == "") {
				return e, fmt.Errorf("only zero-length metadata tombstones may be deleted in sync pipeline: %w", domain.ErrInvalid)
			}
			n := int64(pack.RecordHeaderSize+len(a.LogicalPath)) + a.Size
			if b == nil || b.PlannedBytes > 0 && b.PlannedBytes+n > p.MaxBatchBytes {
				if x := fb(); x != nil {
					return e, x
				}
				bs++
				id, _ := domain.NewID()
				v := domain.Batch{ID: id, EpochID: e.ID, SourceID: e.SourceID, Sequence: bs, Status: domain.BatchPlanned, CreatedAt: time.Now()}
				if x := p.Transfers.CreateBatch(ctx, v); x != nil {
					return e, x
				}
				b = &v
				ps = 0
			}
			if k == nil || k.Size > 0 && k.Size+n > p.TargetPackBytes {
				if x := fp(); x != nil {
					return e, x
				}
				ps++
				id, _ := domain.NewID()
				v := domain.Pack{ID: id, EpochID: e.ID, BatchID: b.ID, Sequence: ps, Status: domain.PackPlanned}
				if x := p.Transfers.CreatePack(ctx, v); x != nil {
					return e, x
				}
				k = &v
				b.PackCount++
			}
			k.Size += n
			k.EntryCount++
			b.PlannedBytes += n
			b.ObjectCount++
			if x := p.Plans.AssignArtifact(ctx, a.ID, b.ID, k.ID); x != nil {
				return e, x
			}
			if x := p.Transfers.AddDownloadEntry(ctx, a, b.ID, k.ID); x != nil {
				return e, x
			}
		}
	}
	if x := fb(); x != nil {
		return e, x
	}
	if bs == 0 {
		batchID, err := domain.NewID()
		if err != nil {
			return e, err
		}
		packID, err := domain.NewID()
		if err != nil {
			return e, err
		}
		b := domain.Batch{ID: batchID, EpochID: e.ID, SourceID: e.SourceID, Sequence: 1, Status: domain.BatchPlanned, PackCount: 1, CreatedAt: time.Now()}
		k := domain.Pack{ID: packID, EpochID: e.ID, BatchID: batchID, Sequence: 1, Status: domain.PackPlanned}
		if err = p.Transfers.CreateBatch(ctx, b); err != nil {
			return e, err
		}
		if err = p.Transfers.CreatePack(ctx, k); err != nil {
			return e, err
		}
		bs = 1
	}
	e.TotalBatches = bs
	if err := p.Transfers.UpdateEpoch(ctx, e); err != nil {
		return e, err
	}
	return e, nil
}
