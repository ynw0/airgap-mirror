package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func (s *ServerStore) RegisterManifest(ctx context.Context, sessionID string) error {
	sess, err := s.GetImportSession(ctx, sessionID)
	if err != nil {
		return err
	}
	ep, err := s.GetEpoch(ctx, sess.EpochID)
	if err != nil {
		return err
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `ATTACH DATABASE ? AS incoming`, sess.ManifestPath); err != nil {
		return fmt.Errorf("attach manifest: %w", err)
	}
	defer conn.ExecContext(context.Background(), `DETACH DATABASE incoming`)
	meta := func(key string) (string, error) {
		var v string
		e := conn.QueryRowContext(ctx, `SELECT value FROM incoming.manifest_meta WHERE key=?`, key).Scan(&v)
		return v, e
	}
	sourceID, err := meta("source_id")
	if err != nil {
		return err
	}
	epochID, err := meta("epoch_id")
	if err != nil {
		return err
	}
	batchID, err := meta("batch_id")
	if err != nil {
		return err
	}
	if sourceID != sess.SourceID || epochID != sess.EpochID || batchID != sess.BatchID {
		return fmt.Errorf("manifest identity mismatch: %w", domain.ErrConflict)
	}
	schemaVersion, err := meta("schema_version")
	if err != nil {
		return err
	}
	if schemaVersion != fmt.Sprint(domain.ManifestSchemaVersion) {
		return fmt.Errorf("unsupported manifest schema %s: %w", schemaVersion, domain.ErrInvalid)
	}
	batch, err := s.GetBatch(ctx, sess.BatchID)
	if err != nil {
		return err
	}
	var invalid int64
	checks := []struct {
		query string
		args  []any
		name  string
	}{
		{`SELECT COUNT(*) FROM incoming.publish_units WHERE epoch_id<>? OR source_id<>? OR required_count<0`, []any{sess.EpochID, sess.SourceID}, "publish unit identity"},
		{`SELECT COUNT(*) FROM incoming.entries WHERE epoch_id<>? OR source_id<>? OR size<0 OR length(sha256)<>64 OR operation NOT IN('ADD','UPDATE','DELETE') OR (operation='DELETE' AND (metadata<>1 OR size<>0 OR lower(sha256)<>?)) OR pack_offset<? OR record_length<=0`, []any{sess.EpochID, sess.SourceID, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", 128}, "entry identity"},
		{`SELECT COUNT(*) FROM incoming.entries e LEFT JOIN incoming.publish_units u ON u.id=e.publish_unit_id WHERE u.id IS NULL`, nil, "entry publish unit"},
		{`SELECT COUNT(*) FROM incoming.entries e LEFT JOIN main.packs p ON p.id=e.pack_id AND p.batch_id=? WHERE p.id IS NULL`, []any{sess.BatchID}, "entry pack"},
		{`SELECT COUNT(*) FROM (SELECT logical_path FROM incoming.entries GROUP BY logical_path HAVING COUNT(*)>1)`, nil, "duplicate logical path"},
		{`SELECT COUNT(*) FROM incoming.publish_units u WHERE u.required_count < (SELECT COUNT(*) FROM incoming.entries e WHERE e.publish_unit_id=u.id)`, nil, "publish unit required count"},
	}
	for _, check := range checks {
		if err = conn.QueryRowContext(ctx, check.query, check.args...).Scan(&invalid); err != nil {
			return err
		}
		if invalid != 0 {
			return fmt.Errorf("manifest %s validation failed (%d rows): %w", check.name, invalid, domain.ErrConflict)
		}
	}
	var entryCount int64
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM incoming.entries`).Scan(&entryCount); err != nil {
		return err
	}
	if entryCount != batch.ObjectCount {
		return fmt.Errorf("manifest entries %d != batch objects %d: %w", entryCount, batch.ObjectCount, domain.ErrConflict)
	}
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM main.packs p WHERE p.batch_id=? AND p.entry_count<>(SELECT COUNT(*) FROM incoming.entries e WHERE e.pack_id=p.id)`, sess.BatchID).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return fmt.Errorf("manifest pack entry counts mismatch (%d packs): %w", invalid, domain.ErrConflict)
	}
	var count int64
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM incoming.publish_units`).Scan(&count); err != nil {
		return err
	}
	if count != ep.PublishUnitCount {
		return fmt.Errorf("manifest publish unit count %d != epoch %d: %w", count, ep.PublishUnitCount, domain.ErrConflict)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var conflicts int64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM incoming.publish_units i JOIN main.publish_units p ON p.id=i.id WHERE p.epoch_id<>i.epoch_id OR p.source_id<>i.source_id OR p.unit_key<>i.unit_key OR p.required_count<>i.required_count OR p.metadata_path<>i.metadata_path`).Scan(&conflicts)
	if err != nil {
		return err
	}
	if conflicts != 0 {
		return fmt.Errorf("publish unit declaration conflict: %w", domain.ErrConflict)
	}
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM incoming.publish_units i JOIN main.publish_units p ON p.epoch_id=i.epoch_id AND p.unit_key=i.unit_key WHERE p.id<>i.id`).Scan(&conflicts)
	if err != nil {
		return err
	}
	if conflicts != 0 {
		return fmt.Errorf("publish unit key conflict: %w", domain.ErrConflict)
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO main.publish_units(id,epoch_id,source_id,unit_key,status,required_count,imported_count,metadata_path) SELECT id,epoch_id,source_id,unit_key,?,required_count,0,metadata_path FROM incoming.publish_units`, domain.PublishWaiting)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE import_sessions SET status='MANIFEST_READY',updated_at=? WHERE id=?`, timeString(time.Now()), sessionID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
