package sqlite

import (
	"context"
	"encoding/json"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func (c *ClientStore) LoadCapsule(ctx context.Context, id string) (domain.StateCapsule, string, map[string]string, error) {
	var cap domain.StateCapsule
	var root string
	var stateJSON []byte
	if err := c.DB.QueryRowContext(ctx, `SELECT file_path,state_json FROM state_capsules WHERE id=?`, id).Scan(&root, &stateJSON); err != nil {
		return cap, "", nil, mapNotFound(err)
	}
	if err := json.Unmarshal(stateJSON, &cap); err != nil {
		return cap, "", nil, err
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT source_id,file_path FROM capsule_catalogs WHERE capsule_id=? ORDER BY source_id`, id)
	if err != nil {
		return cap, "", nil, err
	}
	defer rows.Close()
	paths := make(map[string]string, len(cap.Catalogs))
	for rows.Next() {
		var sourceID, path string
		if err = rows.Scan(&sourceID, &path); err != nil {
			return cap, "", nil, err
		}
		paths[sourceID] = path
	}
	if err = rows.Err(); err != nil {
		return cap, "", nil, err
	}
	if len(paths) != len(cap.Catalogs) {
		return cap, "", nil, domain.ErrConflict
	}
	return cap, root, paths, nil
}

func (c *ClientStore) ListCapsules(ctx context.Context) ([]domain.StateCapsule, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT state_json FROM state_capsules ORDER BY imported_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.StateCapsule
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		var cap domain.StateCapsule
		if err = json.Unmarshal(body, &cap); err != nil {
			return nil, err
		}
		out = append(out, cap)
	}
	return out, rows.Err()
}

func (c *ClientStore) ListBatches(ctx context.Context, epochID string) ([]domain.Batch, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT id,epoch_id,source_id,sequence,status,planned_bytes,object_count,pack_count,created_at FROM download_batches WHERE epoch_id=? ORDER BY sequence`, epochID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Batch
	for rows.Next() {
		var b domain.Batch
		var created string
		if err = rows.Scan(&b.ID, &b.EpochID, &b.SourceID, &b.Sequence, &b.Status, &b.PlannedBytes, &b.ObjectCount, &b.PackCount, &created); err != nil {
			return nil, err
		}
		b.CreatedAt = parseTime(created)
		out = append(out, b)
	}
	return out, rows.Err()
}
