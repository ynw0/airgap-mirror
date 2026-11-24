package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

const reclaimCatalogBatch = 5000

// ReclaimCatalogHistory removes only unreachable metadata, in bounded transactions.
// It never touches repository files, the live generation, or resumable jobs.
// Repeating the operation after interruption is safe.
func (s *ServerStore) ReclaimCatalogHistory(ctx context.Context, sourceID string) (domain.CatalogReclaimResult, error) {
	var out domain.CatalogReclaimResult
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil { return out, err }
	defer tx.Rollback()
	var idle int
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN COALESCE(active_epoch_id,'')='' AND NOT EXISTS(
SELECT 1 FROM maintenance_jobs j WHERE j.source_id=source_states.source_id AND j.status IN('QUEUED','RUNNING','PAUSED')) THEN 1 ELSE 0 END FROM source_states WHERE source_id=?`,sourceID).Scan(&idle)
	if err != nil { return out,mapNotFound(err) }
	if idle!=1 { return out,fmt.Errorf("软件源正在同步或维护，请结束任务后再回收历史账本: %w",domain.ErrConflict) }

	// The original pre-generation table is unreachable only once a valid live
	// pointer exists. Leave it intact for sources not migrated yet.
	var live string
	err=tx.QueryRowContext(ctx, `SELECT a.generation FROM catalog_active_generation a JOIN catalog_generations g ON g.source_id=a.source_id AND g.generation=a.generation AND g.status='LIVE' WHERE a.source_id=?`,sourceID).Scan(&live)
	if err!=nil && err!=sql.ErrNoRows {return out,err}
	if live!="" {
		res,e:=tx.ExecContext(ctx, `DELETE FROM catalog_entries WHERE source_id=? AND logical_path IN(
SELECT logical_path FROM catalog_entries WHERE source_id=? ORDER BY logical_path LIMIT ?)`,sourceID,sourceID,reclaimCatalogBatch)
		if e!=nil{return out,e}
		out.RemovedEntries,e=res.RowsAffected()
		if e!=nil{return out,e}
		if out.RemovedEntries>0 {out.More=true;return out,tx.Commit()}
	}
	// BUILDING generations are eligible only if their owning task is explicitly
	// terminal and unsuccessful. Unknown owners and paused tasks are preserved.
	var generation string
	err=tx.QueryRowContext(ctx, `SELECT g.generation FROM catalog_generations g WHERE g.source_id=?
AND NOT EXISTS(SELECT 1 FROM catalog_active_generation a WHERE a.source_id=g.source_id AND a.generation=g.generation)
AND (g.status='RETIRED' OR (g.status='BUILDING' AND EXISTS(
SELECT 1 FROM maintenance_jobs j WHERE j.id=g.generation AND j.source_id=g.source_id AND j.kind='INVENTORY' AND j.status IN('FAILED','CANCELLED'))))
ORDER BY g.generation LIMIT 1`,sourceID).Scan(&generation)
	if err==sql.ErrNoRows {return out,tx.Commit()}
	if err!=nil{return out,err}
	res,err:=tx.ExecContext(ctx, `DELETE FROM catalog_generation_entries WHERE source_id=? AND generation=? AND logical_path IN(
SELECT logical_path FROM catalog_generation_entries WHERE source_id=? AND generation=? ORDER BY logical_path LIMIT ?)`,sourceID,generation,sourceID,generation,reclaimCatalogBatch)
	if err!=nil{return out,err}
	out.RemovedEntries,err=res.RowsAffected()
	if err!=nil{return out,err}
	if out.RemovedEntries==0 {
		res,err=tx.ExecContext(ctx, `DELETE FROM catalog_generations WHERE source_id=? AND generation=? AND NOT EXISTS(
SELECT 1 FROM catalog_active_generation WHERE source_id=? AND generation=?)`,sourceID,generation,sourceID,generation)
		if err!=nil{return out,err}
		out.RemovedGenerations,err=res.RowsAffected()
		if err!=nil{return out,err}
	}
	out.More=true
	return out,tx.Commit()
}
