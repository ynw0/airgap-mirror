package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
	"time"
)

type ClientStore struct{ DB *sql.DB }

func NewClientStore(db *sql.DB) *ClientStore { return &ClientStore{DB: db} }
func (c *ClientStore) Begin(ctx context.Context, e domain.Epoch, cid string) error {
	_, x := c.DB.ExecContext(ctx, `INSERT INTO download_epochs(id,capsule_id,source_id,base_cursor_kind,base_cursor_value,target_cursor_kind,target_cursor_value,status,total_bytes,total_objects,total_batches,publish_unit_count,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, e.ID, cid, e.SourceID, e.BaseCursor.Kind, e.BaseCursor.Value, e.TargetCursor.Kind, e.TargetCursor.Value, e.Status, e.TotalBytes, e.TotalObjects, e.TotalBatches, e.PublishUnitCount, timeString(e.CreatedAt))
	return x
}
func (c *ClientStore) PutPublishUnit(ctx context.Context, u domain.PublishUnit) error {
	if u.Required < 0 {
		return fmt.Errorf("publish unit required count cannot be negative: %w", domain.ErrConflict)
	}
	tx, e := c.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()

	var epochID, sourceID, key, metadataPath string
	var required int64
	e = tx.QueryRowContext(ctx, `SELECT epoch_id,source_id,unit_key,required_count,metadata_path FROM planned_publish_units WHERE id=?`, u.ID).Scan(&epochID, &sourceID, &key, &required, &metadataPath)
	switch {
	case e == sql.ErrNoRows:
		if _, e = tx.ExecContext(ctx, `INSERT INTO planned_publish_units(id,epoch_id,source_id,unit_key,required_count,metadata_path) VALUES(?,?,?,?,?,?)`, u.ID, u.EpochID, u.SourceID, u.Key, u.Required, u.MetadataPath); e != nil {
			return e
		}
	case e != nil:
		return e
	default:
		if epochID != u.EpochID || sourceID != u.SourceID || key != u.Key || required != u.Required || metadataPath != u.MetadataPath {
			return fmt.Errorf("publish unit declaration changed for %s: %w", u.ID, domain.ErrConflict)
		}
	}
	return tx.Commit()
}
func (c *ClientStore) PutArtifact(ctx context.Context, a domain.Artifact) error {
	_, e := c.DB.ExecContext(ctx, `INSERT INTO planned_artifacts(id,epoch_id,source_id,logical_path,size,sha256,upstream_url,upstream_integrity,local_source_path,operation,publish_unit_id,package_key,version,metadata,attributes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.ID, a.EpochID, a.SourceID, a.LogicalPath, a.Size, a.SHA256, a.UpstreamURL, a.UpstreamIntegrity, a.LocalSourcePath, a.Operation, a.PublishUnitID, a.PackageKey, a.Version, a.Metadata, []byte(a.Attributes))
	return e
}
func (c *ClientStore) Commit(ctx context.Context, p domain.EpochPlan) error {
	tx, e := c.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()

	var artifacts, units, metadata int64
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM planned_artifacts WHERE epoch_id=?`, p.Epoch.ID).Scan(&artifacts); e != nil {
		return e
	}
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM planned_publish_units WHERE epoch_id=?`, p.Epoch.ID).Scan(&units); e != nil {
		return e
	}
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM planned_artifacts WHERE epoch_id=? AND metadata=1`, p.Epoch.ID).Scan(&metadata); e != nil {
		return e
	}
	if artifacts != p.ArtifactCount || units != p.PublishUnitCount || metadata != p.MetadataCount {
		return fmt.Errorf("epoch plan summary mismatch: artifacts=%d/%d units=%d/%d metadata=%d/%d: %w", artifacts, p.ArtifactCount, units, p.PublishUnitCount, metadata, p.MetadataCount, domain.ErrConflict)
	}

	var orphanCount int64
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM planned_artifacts a LEFT JOIN planned_publish_units u ON u.id=a.publish_unit_id AND u.epoch_id=a.epoch_id AND u.source_id=a.source_id WHERE a.epoch_id=? AND (a.publish_unit_id='' OR u.id IS NULL)`, p.Epoch.ID).Scan(&orphanCount); e != nil {
		return e
	}
	if orphanCount != 0 {
		return fmt.Errorf("epoch %s contains %d artifacts without a matching publish unit: %w", p.Epoch.ID, orphanCount, domain.ErrConflict)
	}

	var unitID string
	var required, actual int64
	e = tx.QueryRowContext(ctx, `SELECT u.id,u.required_count,COUNT(a.id) FROM planned_publish_units u LEFT JOIN planned_artifacts a ON a.epoch_id=u.epoch_id AND a.source_id=u.source_id AND a.publish_unit_id=u.id WHERE u.epoch_id=? GROUP BY u.id,u.required_count HAVING COUNT(a.id)<>u.required_count LIMIT 1`, p.Epoch.ID).Scan(&unitID, &required, &actual)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if e == nil {
		return fmt.Errorf("publish unit %s required count mismatch: required=%d actual=%d: %w", unitID, required, actual, domain.ErrConflict)
	}

	r, e := tx.ExecContext(ctx, `UPDATE download_epochs SET target_cursor_kind=?,target_cursor_value=?,status=?,total_bytes=?,total_objects=?,total_batches=?,publish_unit_count=? WHERE id=?`, p.Epoch.TargetCursor.Kind, p.Epoch.TargetCursor.Value, p.Epoch.Status, p.Epoch.TotalBytes, p.Epoch.TotalObjects, p.Epoch.TotalBatches, p.PublishUnitCount, p.Epoch.ID)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return domain.ErrNotFound
	}
	return tx.Commit()
}
func (c *ClientStore) Abort(ctx context.Context, id string, cause error) error {
	_, e := c.DB.ExecContext(ctx, `UPDATE download_epochs SET status=?,error_text=? WHERE id=?`, domain.EpochFailed, cause.Error(), id)
	return e
}
func (c *ClientStore) ListUnassignedArtifacts(ctx context.Context, eid string, after int64, limit int) ([]ports.PlannedArtifact, error) {
	rows, e := c.DB.QueryContext(ctx, `SELECT ordinal,id,source_id,logical_path,size,sha256,upstream_url,upstream_integrity,local_source_path,operation,publish_unit_id,package_key,version,metadata,attributes,batch_id,pack_id FROM planned_artifacts WHERE epoch_id=? AND batch_id='' AND ordinal>? ORDER BY ordinal LIMIT ?`, eid, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []ports.PlannedArtifact
	for rows.Next() {
		var p ports.PlannedArtifact
		var a domain.Artifact
		var op string
		var attr []byte
		if e = rows.Scan(&p.Ordinal, &a.ID, &a.SourceID, &a.LogicalPath, &a.Size, &a.SHA256, &a.UpstreamURL, &a.UpstreamIntegrity, &a.LocalSourcePath, &op, &a.PublishUnitID, &a.PackageKey, &a.Version, &a.Metadata, &attr, &p.BatchID, &p.PackID); e != nil {
			return nil, e
		}
		a.EpochID = eid
		a.Operation = domain.ArtifactOperation(op)
		a.Attributes = attr
		p.Artifact = a
		out = append(out, p)
	}
	return out, rows.Err()
}
func (c *ClientStore) AssignArtifact(ctx context.Context, id, bid, pid string) error {
	_, e := c.DB.ExecContext(ctx, `UPDATE planned_artifacts SET batch_id=?,pack_id=? WHERE id=? AND batch_id=''`, bid, pid, id)
	return e
}
func (c *ClientStore) SetArtifactPackLocation(ctx context.Context, id string, l domain.PackLocation) error {
	_, e := c.DB.ExecContext(ctx, `UPDATE planned_artifacts SET pack_id=?,pack_offset=?,record_length=? WHERE id=?`, l.PackID, l.Offset, l.RecordLength, id)
	return e
}
func (c *ClientStore) FinalizeArtifact(ctx context.Context, id string, size int64, sha string) error {
	tx, e := c.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var os int64
	var oh string
	if e = tx.QueryRowContext(ctx, `SELECT size,sha256 FROM planned_artifacts WHERE id=?`, id).Scan(&os, &oh); e != nil {
		return e
	}
	if os != size || oh != "" && oh != sha {
		return fmt.Errorf("artifact immutable data changed: %w", domain.ErrConflict)
	}
	if _, e = tx.ExecContext(ctx, `UPDATE planned_artifacts SET sha256=? WHERE id=?`, sha, id); e != nil {
		return e
	}
	return tx.Commit()
}
func (c *ClientStore) UpdateEpoch(ctx context.Context, e domain.Epoch) error {
	r, err := c.DB.ExecContext(ctx, `UPDATE download_epochs SET status=?,total_bytes=?,total_objects=?,total_batches=?,publish_unit_count=?,error_text=? WHERE id=?`, e.Status, e.TotalBytes, e.TotalObjects, e.TotalBatches, e.PublishUnitCount, e.ErrorText, e.ID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (c *ClientStore) CreateBatch(ctx context.Context, b domain.Batch) error {
	_, e := c.DB.ExecContext(ctx, `INSERT INTO download_batches(id,epoch_id,source_id,sequence,status,planned_bytes,object_count,pack_count,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, b.ID, b.EpochID, b.SourceID, b.Sequence, b.Status, b.PlannedBytes, b.ObjectCount, b.PackCount, timeString(b.CreatedAt))
	return e
}
func (c *ClientStore) UpdateBatch(ctx context.Context, b domain.Batch) error {
	_, e := c.DB.ExecContext(ctx, `UPDATE download_batches SET status=?,planned_bytes=?,object_count=?,pack_count=? WHERE id=?`, b.Status, b.PlannedBytes, b.ObjectCount, b.PackCount, b.ID)
	return e
}
func (c *ClientStore) CreatePack(ctx context.Context, p domain.Pack) error {
	_, e := c.DB.ExecContext(ctx, `INSERT INTO download_packs(id,epoch_id,batch_id,sequence,size,sha256,entry_count,file_path,status) VALUES(?,?,?,?,?,?,?,?,?)`, p.ID, p.EpochID, p.BatchID, p.Sequence, p.Size, p.SHA256, p.EntryCount, p.FilePath, p.Status)
	return e
}
func (c *ClientStore) UpdatePack(ctx context.Context, p domain.Pack) error {
	_, e := c.DB.ExecContext(ctx, `UPDATE download_packs SET size=?,sha256=?,entry_count=?,file_path=?,status=? WHERE id=?`, p.Size, p.SHA256, p.EntryCount, p.FilePath, p.Status, p.ID)
	return e
}
func (c *ClientStore) AddDownloadEntry(ctx context.Context, a domain.Artifact, bid, pid string) error {
	_, e := c.DB.ExecContext(ctx, `INSERT INTO download_entries(artifact_id,batch_id,pack_id,status,downloaded) VALUES(?,?,?,?,0)`, a.ID, bid, pid, domain.DownloadPending)
	return e
}
func (c *ClientStore) EntryState(ctx context.Context, id string) (domain.DownloadEntryProgress, error) {
	var v domain.DownloadEntryProgress
	e := c.DB.QueryRowContext(ctx, `SELECT artifact_id,status,downloaded,etag,last_modified,error_text FROM download_entries WHERE artifact_id=?`, id).Scan(&v.EntryID, &v.Status, &v.Downloaded, &v.ETag, &v.LastModified, &v.ErrorText)
	if e != nil {
		return v, mapNotFound(e)
	}
	return v, nil
}
func (c *ClientStore) MarkEntryState(ctx context.Context, v domain.DownloadEntryProgress) error {
	_, e := c.DB.ExecContext(ctx, `UPDATE download_entries SET status=?,downloaded=?,etag=?,last_modified=?,error_text=? WHERE artifact_id=?`, v.Status, v.Downloaded, v.ETag, v.LastModified, v.ErrorText, v.EntryID)
	return e
}
func (c *ClientStore) Reset(ctx context.Context, ns string) error {
	_, e := c.DB.ExecContext(ctx, `DELETE FROM workset_keys WHERE namespace=?`, ns)
	return e
}
func (c *ClientStore) Add(ctx context.Context, ns, key string) (bool, error) {
	r, e := c.DB.ExecContext(ctx, `INSERT OR IGNORE INTO workset_keys(namespace,key,value) VALUES(?,?,'')`, ns, key)
	if e != nil {
		return false, e
	}
	n, _ := r.RowsAffected()
	return n == 1, nil
}
func (c *ClientStore) Walk(ctx context.Context, ns string, fn func(string) error) error {
	rows, e := c.DB.QueryContext(ctx, `SELECT key FROM workset_keys WHERE namespace=? ORDER BY key`, ns)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if e = rows.Scan(&k); e != nil {
			return e
		}
		if e = fn(k); e != nil {
			return e
		}
	}
	return rows.Err()
}

func (c *ClientStore) Put(ctx context.Context, ns, key, value string) error {
	_, e := c.DB.ExecContext(ctx, `INSERT INTO workset_keys(namespace,key,value) VALUES(?,?,?) ON CONFLICT(namespace,key) DO UPDATE SET value=excluded.value`, ns, key, value)
	return e
}
func (c *ClientStore) WalkValues(ctx context.Context, ns string, fn func(string, string) error) error {
	rows, e := c.DB.QueryContext(ctx, `SELECT key,value FROM workset_keys WHERE namespace=? ORDER BY key`, ns)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if e = rows.Scan(&key, &value); e != nil {
			return e
		}
		if e = fn(key, value); e != nil {
			return e
		}
	}
	return rows.Err()
}
func (c *ClientStore) StoreCapsule(ctx context.Context, cap domain.StateCapsule, path string, cats map[string]string) error {
	b, e := json.Marshal(cap)
	if e != nil {
		return e
	}
	tx, e := c.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `INSERT INTO state_capsules(id,file_path,exported_at,imported_at,state_json) VALUES(?,?,?,?,?)`, cap.ID, path, timeString(cap.ExportedAt), timeString(time.Now()), b); e != nil {
		return e
	}
	for _, x := range cap.Catalogs {
		if _, e = tx.ExecContext(ctx, `INSERT INTO capsule_catalogs(capsule_id,source_id,file_path,sha256,size) VALUES(?,?,?,?,?)`, cap.ID, x.SourceID, cats[x.SourceID], x.SHA256, x.Size); e != nil {
			return e
		}
	}
	return tx.Commit()
}
