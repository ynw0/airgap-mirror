package client

import (
	"context"
	"fmt"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type WorkspaceTransferResult struct {
	Remote BundleTransferResult `json:"remote"`
	Batch  domain.Batch         `json:"batch"`
	Epoch  domain.Epoch         `json:"epoch"`
}

func batchAtTransferBoundary(status domain.BatchStatus) bool {
	switch status {
	case domain.BatchReady, domain.BatchTransferring, domain.BatchImporting, domain.BatchImported:
		return true
	default:
		return false
	}
}

func allBatchesTransferReady(batches []domain.Batch, expected int) bool {
	if expected <= 0 || len(batches) != expected {
		return false
	}
	for _, batch := range batches {
		if !batchAtTransferBoundary(batch.Status) {
			return false
		}
	}
	return true
}

func allBatchesImported(batches []domain.Batch, expected int) bool {
	if expected <= 0 || len(batches) != expected {
		return false
	}
	for _, batch := range batches {
		if batch.Status != domain.BatchImported {
			return false
		}
	}
	return true
}

func sameLocalBundle(epoch domain.Epoch, batch domain.Batch, d domain.BatchDescriptor) bool {
	return epoch.ID == d.EpochID &&
		epoch.SourceID == d.SourceID &&
		epoch.BaseCursor.Equal(d.BaseCursor) &&
		epoch.TargetCursor.Equal(d.TargetCursor) &&
		epoch.TotalBytes == d.EpochTotalBytes &&
		epoch.TotalObjects == d.EpochTotalObjects &&
		epoch.TotalBatches == d.EpochTotalBatches &&
		epoch.PublishUnitCount == d.EpochPublishUnitCount &&
		batch.ID == d.BatchID &&
		batch.EpochID == d.EpochID &&
		batch.SourceID == d.SourceID &&
		batch.Sequence == d.BatchSequence &&
		batch.PlannedBytes == d.BatchBytes &&
		batch.ObjectCount == d.BatchObjects &&
		batch.PackCount == len(d.Packs)
}

func (w *Workspace) moveEpochToTransferringIfReady(ctx context.Context, epoch domain.Epoch) (domain.Epoch, error) {
	if epoch.Status != domain.EpochDownloading {
		return epoch, nil
	}
	batches, err := w.Store.ListBatches(ctx, epoch.ID)
	if err != nil {
		return epoch, err
	}
	if !allBatchesTransferReady(batches, epoch.TotalBatches) {
		return epoch, nil
	}
	if err = domain.ValidateEpochTransition(epoch.Status, domain.EpochTransferring); err != nil {
		return epoch, err
	}
	epoch.Status = domain.EpochTransferring
	if err = w.Store.UpdateEpoch(ctx, epoch); err != nil {
		return epoch, err
	}
	return epoch, nil
}

func (w *Workspace) markLocalBatchImported(ctx context.Context, batch domain.Batch) (domain.Batch, error) {
	var err error
	if batch.Status == domain.BatchReady {
		if err = domain.ValidateBatchTransition(batch.Status, domain.BatchTransferring); err != nil {
			return batch, err
		}
		batch.Status = domain.BatchTransferring
		if err = w.Store.UpdateBatch(ctx, batch); err != nil {
			return batch, err
		}
	}
	if batch.Status == domain.BatchTransferring {
		if err = domain.ValidateBatchTransition(batch.Status, domain.BatchImporting); err != nil {
			return batch, err
		}
		batch.Status = domain.BatchImporting
		if err = w.Store.UpdateBatch(ctx, batch); err != nil {
			return batch, err
		}
	}
	if batch.Status == domain.BatchImporting {
		if err = domain.ValidateBatchTransition(batch.Status, domain.BatchImported); err != nil {
			return batch, err
		}
		batch.Status = domain.BatchImported
		if err = w.Store.UpdateBatch(ctx, batch); err != nil {
			return batch, err
		}
	}
	if batch.Status != domain.BatchImported {
		return batch, fmt.Errorf("batch %s cannot reconcile imported state from %s: %w", batch.ID, batch.Status, domain.ErrConflict)
	}
	return batch, nil
}

func (w *Workspace) completeLocalEpochIfServerAdvanced(ctx context.Context, agent *AgentClient, epoch domain.Epoch) (domain.Epoch, error) {
	batches, err := w.Store.ListBatches(ctx, epoch.ID)
	if err != nil {
		return epoch, err
	}
	if !allBatchesImported(batches, epoch.TotalBatches) {
		return epoch, nil
	}
	state, err := agent.SourceState(ctx, epoch.SourceID)
	if err != nil {
		return epoch, err
	}
	if !state.LiveCursor.Equal(epoch.TargetCursor) || state.ActiveEpochID != "" {
		return epoch, fmt.Errorf("all local batches imported but server source has cursor=%v activeEpoch=%q, expected target=%v and no active epoch: %w", state.LiveCursor, state.ActiveEpochID, epoch.TargetCursor, domain.ErrConflict)
	}
	if epoch.Status == domain.EpochDownloading {
		epoch, err = w.moveEpochToTransferringIfReady(ctx, epoch)
		if err != nil {
			return epoch, err
		}
	}
	for epoch.Status != domain.EpochComplete {
		var next domain.EpochStatus
		switch epoch.Status {
		case domain.EpochTransferring:
			next = domain.EpochPublishable
		case domain.EpochPublishable:
			next = domain.EpochPublished
		case domain.EpochPublished:
			next = domain.EpochComplete
		default:
			return epoch, fmt.Errorf("local epoch %s cannot reconcile completion from %s: %w", epoch.ID, epoch.Status, domain.ErrConflict)
		}
		if err = domain.ValidateEpochTransition(epoch.Status, next); err != nil {
			return epoch, err
		}
		epoch.Status = next
		if err = w.Store.UpdateEpoch(ctx, epoch); err != nil {
			return epoch, err
		}
	}
	return epoch, nil
}

func (w *Workspace) TransferLocalBundle(ctx context.Context, agent *AgentClient, bundleDir string, opts BundleTransferOptions, progress ProgressFunc) (WorkspaceTransferResult, error) {
	var out WorkspaceTransferResult
	if agent == nil {
		return out, fmt.Errorf("agent client is required: %w", domain.ErrInvalid)
	}
	validated, err := ValidateBatchBundle(ctx, bundleDir)
	if err != nil {
		return out, err
	}
	descriptor := validated.Descriptor
	batch, err := w.Store.GetBatch(ctx, descriptor.BatchID)
	if err != nil {
		return out, err
	}
	epoch, err := w.Store.GetEpoch(ctx, descriptor.EpochID)
	if err != nil {
		return out, err
	}
	if !sameLocalBundle(epoch, batch, descriptor) {
		return out, fmt.Errorf("bundle immutable fields differ from local workspace: %w", domain.ErrConflict)
	}
	if epoch.Status != domain.EpochDownloading && epoch.Status != domain.EpochTransferring && epoch.Status != domain.EpochPublishable && epoch.Status != domain.EpochPublished && epoch.Status != domain.EpochComplete {
		return out, fmt.Errorf("local epoch %s is %s and cannot transfer: %w", epoch.ID, epoch.Status, domain.ErrConflict)
	}
	if !batchAtTransferBoundary(batch.Status) {
		return out, fmt.Errorf("local batch %s is %s and is not ready for transfer: %w", batch.ID, batch.Status, domain.ErrConflict)
	}
	if batch.Status == domain.BatchReady {
		if err = domain.ValidateBatchTransition(batch.Status, domain.BatchTransferring); err != nil {
			return out, err
		}
		batch.Status = domain.BatchTransferring
		if err = w.Store.UpdateBatch(ctx, batch); err != nil {
			return out, err
		}
	}
	if epoch.Status == domain.EpochDownloading {
		epoch, err = w.moveEpochToTransferringIfReady(ctx, epoch)
		if err != nil {
			return out, err
		}
	}
	remote, err := TransferBatchBundle(ctx, agent, bundleDir, opts, progress)
	if err != nil {
		return out, err
	}
	batch, err = w.Store.GetBatch(ctx, batch.ID)
	if err != nil {
		return out, err
	}
	batch, err = w.markLocalBatchImported(ctx, batch)
	if err != nil {
		return out, err
	}
	epoch, err = w.Store.GetEpoch(ctx, epoch.ID)
	if err != nil {
		return out, err
	}
	epoch, err = w.completeLocalEpochIfServerAdvanced(ctx, agent, epoch)
	if err != nil {
		return out, err
	}
	return WorkspaceTransferResult{Remote: remote, Batch: batch, Epoch: epoch}, nil
}
