package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"time"
)

func (c *ClientStore) GetEpoch(ctx context.Context, id string) (domain.Epoch, error) {
	var e domain.Epoch
	var bk, bv, tk, tv, cr string
	x := c.DB.QueryRowContext(ctx, `SELECT id,source_id,base_cursor_kind,base_cursor_value,target_cursor_kind,target_cursor_value,status,total_bytes,total_objects,total_batches,publish_unit_count,created_at,error_text FROM download_epochs WHERE id=?`, id).Scan(&e.ID, &e.SourceID, &bk, &bv, &tk, &tv, &e.Status, &e.TotalBytes, &e.TotalObjects, &e.TotalBatches, &e.PublishUnitCount, &cr, &e.ErrorText)
	if x != nil {
		return e, mapNotFound(x)
	}
	e.BaseCursor = domain.Cursor{Kind: bk, Value: bv}
	e.TargetCursor = domain.Cursor{Kind: tk, Value: tv}
	e.CreatedAt, _ = time.Parse(time.RFC3339Nano, cr)
	return e, nil
}
func (c *ClientStore) GetBatch(ctx context.Context, id string) (domain.Batch, error) {
	var b domain.Batch
	var cr string
	e := c.DB.QueryRowContext(ctx, `SELECT id,epoch_id,source_id,sequence,status,planned_bytes,object_count,pack_count,created_at FROM download_batches WHERE id=?`, id).Scan(&b.ID, &b.EpochID, &b.SourceID, &b.Sequence, &b.Status, &b.PlannedBytes, &b.ObjectCount, &b.PackCount, &cr)
	if e != nil {
		return b, mapNotFound(e)
	}
	b.CreatedAt, _ = time.Parse(time.RFC3339Nano, cr)
	return b, nil
}
func (c *ClientStore) ListPacks(ctx context.Context, bid string) ([]domain.Pack, error) {
	r, e := c.DB.QueryContext(ctx, `SELECT id,epoch_id,batch_id,sequence,size,sha256,entry_count,file_path,status FROM download_packs WHERE batch_id=? ORDER BY sequence`, bid)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	var out []domain.Pack
	for r.Next() {
		var p domain.Pack
		if e = r.Scan(&p.ID, &p.EpochID, &p.BatchID, &p.Sequence, &p.Size, &p.SHA256, &p.EntryCount, &p.FilePath, &p.Status); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, r.Err()
}
func (c *ClientStore) WalkPackArtifacts(ctx context.Context, pid string, fn func(domain.Artifact) error) error {
	r, e := c.DB.QueryContext(ctx, `SELECT id,epoch_id,source_id,logical_path,size,sha256,upstream_url,upstream_integrity,local_source_path,operation,publish_unit_id,package_key,version,metadata,attributes FROM planned_artifacts WHERE pack_id=? ORDER BY ordinal`, pid)
	if e != nil {
		return e
	}
	defer r.Close()
	for r.Next() {
		var a domain.Artifact
		var op string
		var at []byte
		if e = r.Scan(&a.ID, &a.EpochID, &a.SourceID, &a.LogicalPath, &a.Size, &a.SHA256, &a.UpstreamURL, &a.UpstreamIntegrity, &a.LocalSourcePath, &op, &a.PublishUnitID, &a.PackageKey, &a.Version, &a.Metadata, &at); e != nil {
			return e
		}
		a.Operation = domain.ArtifactOperation(op)
		a.Attributes = at
		if e = fn(a); e != nil {
			return e
		}
	}
	return r.Err()
}
func (c *ClientStore) ListPlannedPublishUnits(ctx context.Context, eid string) ([]domain.PublishUnit, error) {
	r, e := c.DB.QueryContext(ctx, `SELECT id,epoch_id,source_id,unit_key,required_count,metadata_path FROM planned_publish_units WHERE epoch_id=? ORDER BY unit_key`, eid)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	var out []domain.PublishUnit
	for r.Next() {
		var u domain.PublishUnit
		if e = r.Scan(&u.ID, &u.EpochID, &u.SourceID, &u.Key, &u.Required, &u.MetadataPath); e != nil {
			return nil, e
		}
		u.Status = domain.PublishWaiting
		out = append(out, u)
	}
	return out, r.Err()
}
func (c *ClientStore) WalkBatchManifestEntries(ctx context.Context, bid string, fn func(domain.ManifestEntry) error) error {
	r, e := c.DB.QueryContext(ctx, `SELECT id,epoch_id,source_id,logical_path,size,sha256,upstream_url,upstream_integrity,local_source_path,operation,publish_unit_id,package_key,version,metadata,attributes,pack_id,pack_offset,record_length FROM planned_artifacts WHERE batch_id=? ORDER BY ordinal`, bid)
	if e != nil {
		return e
	}
	defer r.Close()
	for r.Next() {
		var m domain.ManifestEntry
		var a domain.Artifact
		var op string
		var at []byte
		var o, l sql.NullInt64
		if e = r.Scan(&a.ID, &a.EpochID, &a.SourceID, &a.LogicalPath, &a.Size, &a.SHA256, &a.UpstreamURL, &a.UpstreamIntegrity, &a.LocalSourcePath, &op, &a.PublishUnitID, &a.PackageKey, &a.Version, &a.Metadata, &at, &m.PackID, &o, &l); e != nil {
			return e
		}
		if !o.Valid || !l.Valid {
			return fmt.Errorf("artifact %s has no pack location", a.ID)
		}
		a.Operation = domain.ArtifactOperation(op)
		a.Attributes = at
		m.EntryID = a.ID
		m.Artifact = a
		m.PackOffset = o.Int64
		m.RecordLength = l.Int64
		if e = fn(m); e != nil {
			return e
		}
	}
	return r.Err()
}
