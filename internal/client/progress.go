package client

import (
	"context"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type BatchDownloadProgress struct {
	Batch           domain.Batch `json:"batch"`
	TotalBytes      int64        `json:"totalBytes"`
	DownloadedBytes int64        `json:"downloadedBytes"`
	TotalEntries    int64        `json:"totalEntries"`
	VerifiedEntries int64        `json:"verifiedEntries"`
	FailedEntries   int64        `json:"failedEntries"`
}

func (w *Workspace) BatchProgress(ctx context.Context, batchID string) (BatchDownloadProgress, error) {
	var out BatchDownloadProgress
	batch, err := w.Store.GetBatch(ctx, batchID)
	if err != nil {
		return out, err
	}
	stats, err := w.Store.DownloadBatchStats(ctx, batchID)
	if err != nil {
		return out, err
	}
	out = BatchDownloadProgress{
		Batch:           batch,
		TotalBytes:      stats.TotalBytes,
		DownloadedBytes: stats.DownloadedBytes,
		TotalEntries:    stats.TotalEntries,
		VerifiedEntries: stats.VerifiedEntries,
		FailedEntries:   stats.FailedEntries,
	}
	return out, nil
}

func (w *Workspace) ListEpochs(ctx context.Context, capsuleID, sourceID string) ([]domain.Epoch, error) {
	return w.Store.ListEpochsForCapsule(ctx, capsuleID, sourceID)
}
