package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
	"github.com/ynw0/airgap-mirror/internal/ports"
	"os"
	"path/filepath"
	"time"
)

type BundleFinalizer struct {
	Exec       ports.TransferExecutionStore
	Plan       ports.BundlePlanStore
	OpenSQLite func(string) (*sql.DB, error)
}

func (f BundleFinalizer) Finalize(ctx context.Context, bid, out string) (domain.BatchDescriptor, error) {
	var d domain.BatchDescriptor
	b, e := f.Exec.GetBatch(ctx, bid)
	if e != nil { return d, e }
	ep, e := f.Exec.GetEpoch(ctx, b.EpochID)
	if e != nil { return d, e }
	ps, e := f.Exec.ListPacks(ctx, bid)
	if e != nil { return d, e }
	for _, p := range ps { if p.Status != domain.PackReady || p.SHA256 == "" { return d, fmt.Errorf("pack %s not ready", p.ID) } }
	if f.OpenSQLite == nil { return d, fmt.Errorf("sqlite opener required") }
	mp := filepath.Join(out, "manifest.sqlite")
	os.Remove(mp)
	db, e := f.OpenSQLite(mp)
	if e != nil { return d, e }
	if e = pack.InitManifest(ctx, db); e != nil { db.Close(); return d, e }
	tx, e := db.BeginTx(ctx, nil)
	if e != nil { db.Close(); return d, e }
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `INSERT INTO manifest_meta(key,value)VALUES('schema_version','1'),('source_id',?),('epoch_id',?),('batch_id',?)`, b.SourceID, b.EpochID, b.ID); e != nil { return d, e }
	units, e := f.Plan.ListPlannedPublishUnits(ctx, b.EpochID)
	if e != nil { return d, e }
	for _, u := range units { if _, e = tx.ExecContext(ctx, `INSERT INTO publish_units(id,epoch_id,source_id,unit_key,required_count,metadata_path)VALUES(?,?,?,?,?,?)`, u.ID, u.EpochID, u.SourceID, u.Key, u.Required, u.MetadataPath); e != nil { return d, e } }
	if e = f.Plan.WalkBatchManifestEntries(ctx, b.ID, func(m domain.ManifestEntry) error {
		a := m.Artifact
		if a.SHA256 == "" { return fmt.Errorf("artifact %s not finalized", a.ID) }
		_, x := tx.ExecContext(ctx, `INSERT INTO entries(entry_id,pack_id,pack_offset,record_length,source_id,epoch_id,logical_path,size,sha256,operation,publish_unit_id,package_key,version,metadata,attributes)VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, m.EntryID,m.PackID,m.PackOffset,m.RecordLength,a.SourceID,a.EpochID,a.LogicalPath,a.Size,a.SHA256,a.Operation,a.PublishUnitID,a.PackageKey,a.Version,a.Metadata,[]byte(a.Attributes))
		return x
	}); e != nil { return d, e }
	if e = tx.Commit(); e != nil { return d, e }
	if e = db.Close(); e != nil { return d, e }
	mh, ms, e := pack.FileSHA256(mp)
	if e != nil { return d, e }
	d = domain.BatchDescriptor{SchemaVersion:1,SourceID:b.SourceID,EpochID:b.EpochID,BatchID:b.ID,BatchSequence:b.Sequence,EpochTotalBytes:ep.TotalBytes,EpochTotalObjects:ep.TotalObjects,EpochTotalBatches:ep.TotalBatches,EpochPublishUnitCount:ep.PublishUnitCount,BatchBytes:b.PlannedBytes,BatchObjects:b.ObjectCount,BaseCursor:ep.BaseCursor,TargetCursor:ep.TargetCursor,ManifestSHA256:mh,ManifestSize:ms,CreatedAt:time.Now()}
	for _, p := range ps { d.Packs = append(d.Packs, domain.PackSpec{ID:p.ID,Sequence:p.Sequence,File:filepath.ToSlash(filepath.Join("packs",fmt.Sprintf("%06d.agp",p.Sequence))),Size:p.Size,SHA256:p.SHA256,EntryCount:p.EntryCount}) }
	body, e := json.MarshalIndent(d,"","  ")
	if e != nil { return d, e }
	if e = os.WriteFile(filepath.Join(out,"batch.json"),body,0644); e != nil { return d, e }
	b.Status=domain.BatchReady
	if e=f.Exec.UpdateBatch(ctx,b); e!=nil { return d,e }
	return d,nil
}
