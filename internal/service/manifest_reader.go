package service

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/ynw0/airgap-mirror/internal/domain"
)

type SQLiteOpener func(string)(*sql.DB,error)
type ManifestReader struct{Open SQLiteOpener}
func(m ManifestReader)WalkPack(ctx context.Context,path,packID string,fn func(domain.ManifestEntry)error)error{
	if m.Open==nil{return fmt.Errorf("manifest sqlite opener is required")}
	db,err:=m.Open(path);if err!=nil{return err};defer db.Close()
	rows,err:=db.QueryContext(ctx,`SELECT entry_id,pack_id,pack_offset,record_length,source_id,epoch_id,logical_path,size,sha256,operation,publish_unit_id,package_key,version,metadata,attributes FROM entries WHERE pack_id=? ORDER BY pack_offset`,packID);if err!=nil{return err};defer rows.Close()
	for rows.Next(){var x domain.ManifestEntry;var op string;var attrs []byte;var metadata bool;if err=rows.Scan(&x.EntryID,&x.PackID,&x.PackOffset,&x.RecordLength,&x.Artifact.SourceID,&x.Artifact.EpochID,&x.Artifact.LogicalPath,&x.Artifact.Size,&x.Artifact.SHA256,&op,&x.Artifact.PublishUnitID,&x.Artifact.PackageKey,&x.Artifact.Version,&metadata,&attrs);err!=nil{return err};x.Artifact.ID=x.EntryID;x.Artifact.Operation=domain.ArtifactOperation(op);x.Artifact.Metadata=metadata;x.Artifact.Attributes=attrs;if err=fn(x);err!=nil{return err}}
	return rows.Err()
}
