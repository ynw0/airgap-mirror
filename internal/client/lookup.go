package client

import (
	"context"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func (w *Workspace) Batch(ctx context.Context, batchID string) (domain.Batch, error) {
	return w.Store.GetBatch(ctx, batchID)
}
