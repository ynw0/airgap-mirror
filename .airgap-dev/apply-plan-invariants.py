from pathlib import Path

path = Path("internal/storage/sqlite/client.go")
s = path.read_text()

old_put = '''func (c *ClientStore) PutPublishUnit(ctx context.Context, u domain.PublishUnit) error {
\t_, e := c.DB.ExecContext(ctx, `INSERT INTO planned_publish_units(id,epoch_id,source_id,unit_key,required_count,metadata_path) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET required_count=excluded.required_count,metadata_path=excluded.metadata_path`, u.ID, u.EpochID, u.SourceID, u.Key, u.Required, u.MetadataPath)
\treturn e
}'''
new_put = '''func (c *ClientStore) PutPublishUnit(ctx context.Context, u domain.PublishUnit) error {
\tif u.Required < 0 {
\t\treturn fmt.Errorf("publish unit required count cannot be negative: %w", domain.ErrConflict)
\t}
\ttx, e := c.DB.BeginTx(ctx, nil)
\tif e != nil {
\t\treturn e
\t}
\tdefer tx.Rollback()

\tvar epochID, sourceID, key, metadataPath string
\tvar required int64
\te = tx.QueryRowContext(ctx, `SELECT epoch_id,source_id,unit_key,required_count,metadata_path FROM planned_publish_units WHERE id=?`, u.ID).Scan(&epochID, &sourceID, &key, &required, &metadataPath)
\tswitch {
\tcase e == sql.ErrNoRows:
\t\tif _, e = tx.ExecContext(ctx, `INSERT INTO planned_publish_units(id,epoch_id,source_id,unit_key,required_count,metadata_path) VALUES(?,?,?,?,?,?)`, u.ID, u.EpochID, u.SourceID, u.Key, u.Required, u.MetadataPath); e != nil {
\t\t\treturn e
\t\t}
\tcase e != nil:
\t\treturn e
\tdefault:
\t\tif epochID != u.EpochID || sourceID != u.SourceID || key != u.Key || required != u.Required || metadataPath != u.MetadataPath {
\t\t\treturn fmt.Errorf("publish unit declaration changed for %s: %w", u.ID, domain.ErrConflict)
\t\t}
\t}
\treturn tx.Commit()
}'''

old_commit = '''func (c *ClientStore) Commit(ctx context.Context, p domain.EpochPlan) error {
\t_, e := c.DB.ExecContext(ctx, `UPDATE download_epochs SET target_cursor_kind=?,target_cursor_value=?,status=?,total_bytes=?,total_objects=?,total_batches=?,publish_unit_count=? WHERE id=?`, p.Epoch.TargetCursor.Kind, p.Epoch.TargetCursor.Value, p.Epoch.Status, p.Epoch.TotalBytes, p.Epoch.TotalObjects, p.Epoch.TotalBatches, p.PublishUnitCount, p.Epoch.ID)
\treturn e
}'''
new_commit = '''func (c *ClientStore) Commit(ctx context.Context, p domain.EpochPlan) error {
\ttx, e := c.DB.BeginTx(ctx, nil)
\tif e != nil {
\t\treturn e
\t}
\tdefer tx.Rollback()

\tvar artifacts, units, metadata int64
\tif e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM planned_artifacts WHERE epoch_id=?`, p.Epoch.ID).Scan(&artifacts); e != nil {
\t\treturn e
\t}
\tif e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM planned_publish_units WHERE epoch_id=?`, p.Epoch.ID).Scan(&units); e != nil {
\t\treturn e
\t}
\tif e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM planned_artifacts WHERE epoch_id=? AND metadata=1`, p.Epoch.ID).Scan(&metadata); e != nil {
\t\treturn e
\t}
\tif artifacts != p.ArtifactCount || units != p.PublishUnitCount || metadata != p.MetadataCount {
\t\treturn fmt.Errorf("epoch plan summary mismatch: artifacts=%d/%d units=%d/%d metadata=%d/%d: %w", artifacts, p.ArtifactCount, units, p.PublishUnitCount, metadata, p.MetadataCount, domain.ErrConflict)
\t}

\tvar orphanCount int64
\tif e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM planned_artifacts a LEFT JOIN planned_publish_units u ON u.id=a.publish_unit_id AND u.epoch_id=a.epoch_id AND u.source_id=a.source_id WHERE a.epoch_id=? AND (a.publish_unit_id='' OR u.id IS NULL)`, p.Epoch.ID).Scan(&orphanCount); e != nil {
\t\treturn e
\t}
\tif orphanCount != 0 {
\t\treturn fmt.Errorf("epoch %s contains %d artifacts without a matching publish unit: %w", p.Epoch.ID, orphanCount, domain.ErrConflict)
\t}

\tvar unitID string
\tvar required, actual int64
\te = tx.QueryRowContext(ctx, `SELECT u.id,u.required_count,COUNT(a.id) FROM planned_publish_units u LEFT JOIN planned_artifacts a ON a.epoch_id=u.epoch_id AND a.source_id=u.source_id AND a.publish_unit_id=u.id WHERE u.epoch_id=? GROUP BY u.id,u.required_count HAVING COUNT(a.id)<>u.required_count LIMIT 1`, p.Epoch.ID).Scan(&unitID, &required, &actual)
\tif e != nil && e != sql.ErrNoRows {
\t\treturn e
\t}
\tif e == nil {
\t\treturn fmt.Errorf("publish unit %s required count mismatch: required=%d actual=%d: %w", unitID, required, actual, domain.ErrConflict)
\t}

\tr, e := tx.ExecContext(ctx, `UPDATE download_epochs SET target_cursor_kind=?,target_cursor_value=?,status=?,total_bytes=?,total_objects=?,total_batches=?,publish_unit_count=? WHERE id=?`, p.Epoch.TargetCursor.Kind, p.Epoch.TargetCursor.Value, p.Epoch.Status, p.Epoch.TotalBytes, p.Epoch.TotalObjects, p.Epoch.TotalBatches, p.PublishUnitCount, p.Epoch.ID)
\tif e != nil {
\t\treturn e
\t}
\tn, e := r.RowsAffected()
\tif e != nil {
\t\treturn e
\t}
\tif n != 1 {
\t\treturn domain.ErrNotFound
\t}
\treturn tx.Commit()
}'''

if old_put not in s:
    raise SystemExit("PutPublishUnit source shape changed")
if old_commit not in s:
    raise SystemExit("Commit source shape changed")

path.write_text(s.replace(old_put, new_put).replace(old_commit, new_commit))
