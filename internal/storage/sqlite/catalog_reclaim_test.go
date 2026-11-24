package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestReclaimCatalogHistoryPreservesLiveAndPausedGenerations(t *testing.T){
	ctx:=context.Background()
	s:=openServerDB(t)
	src,other:=testSource(t),testSource(t)
	for _,v:=range []domain.Source{src,other}{if err:=s.CreateSource(ctx,v);err!=nil{t.Fatal(err)}}
	now:=time.Now()
	_,err:=s.DB.ExecContext(ctx,`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
INSERT INTO catalog_entries(source_id,logical_path,size,sha256,updated_at) SELECT ?,printf('old/%08d',i),1,?,? FROM n`,reclaimCatalogBatch+1,src.ID,shaOf("old"),timeString(now))
	if err!=nil{t.Fatal(err)}
	if err=s.Upsert(ctx,domain.CatalogEntry{SourceID:other.ID,LogicalPath:"legacy.bin",Size:2,SHA256:shaOf("other"),UpdatedAt:now});err!=nil{t.Fatal(err)}
	for _,name:=range []string{"first","second"}{
		path:=writeRebuildCatalog(t,src.ID,name+".bin",7)
		if err=s.ReplaceCatalogFromFile(ctx,src.ID,path,domain.CatalogStats{SourceID:src.ID,Objects:1,Bytes:7},now);err!=nil{t.Fatal(err)}
	}
	job:=domain.MaintenanceJob{ID:mustID(t),SourceID:src.ID,Kind:domain.MaintenanceInventory,Status:domain.MaintenanceQueued,CreatedAt:now}
	if err=s.CreateMaintenanceJob(ctx,job);err!=nil{t.Fatal(err)}
	if err=s.TransitionMaintenanceJob(ctx,job.ID,domain.MaintenancePaused,"pause",now);err!=nil{t.Fatal(err)}
	if _,err=s.DB.ExecContext(ctx,`INSERT INTO catalog_generations(source_id,generation,expected_objects,expected_bytes,status) VALUES(?,?,1,1,'BUILDING')`,src.ID,job.ID);err!=nil{t.Fatal(err)}
	if _,err=s.DB.ExecContext(ctx,`INSERT INTO catalog_generation_entries(source_id,generation,logical_path,size,sha256,updated_at) VALUES(?,?,?,1,?,?)`,src.ID,job.ID,"unfinished.bin",shaOf("x"),timeString(now));err!=nil{t.Fatal(err)}
	if _,err=s.ReclaimCatalogHistory(ctx,src.ID);!errors.Is(err,domain.ErrConflict){t.Fatalf("paused job was not protected: %v",err)}
	if err=s.TransitionMaintenanceJob(ctx,job.ID,domain.MaintenanceCancelled,"cancel",now);err!=nil{t.Fatal(err)}
	first,err:=s.ReclaimCatalogHistory(ctx,src.ID)
	if err!=nil||first.RemovedEntries!=reclaimCatalogBatch||!first.More{t.Fatalf("unexpected first batch: %+v %v",first,err)}
	// Simulate interruption: subsequent calls continue with remaining rows.
	removed:=first.RemovedEntries
	for n:=0;n<10;n++{
		r,e:=s.ReclaimCatalogHistory(ctx,src.ID)
		if e!=nil{t.Fatal(e)}
		removed+=r.RemovedEntries
		if !r.More{break}
		if n==9{t.Fatal("reclaim did not finish")}
	}
	if removed!=reclaimCatalogBatch+3{t.Fatalf("removed %d records",removed)}
	stats,err:=s.Stats(ctx,src.ID)
	if err!=nil||stats.Objects!=1||stats.Bytes!=7{t.Fatalf("live stats changed: %+v %v",stats,err)}
	if _,err=s.Get(ctx,src.ID,"second.bin");err!=nil{t.Fatal(err)}
	if _,err=s.Get(ctx,other.ID,"legacy.bin");err!=nil{t.Fatal(err)}
	if r,e:=s.ReclaimCatalogHistory(ctx,other.ID);e!=nil||r.More||r.RemovedEntries!=0{t.Fatalf("unmigrated legacy catalog was reclaimed: %+v %v",r,e)}
	var count int
	if err=s.DB.QueryRowContext(ctx,"SELECT COUNT(*) FROM catalog_generations WHERE source_id=?",src.ID).Scan(&count);err!=nil||count!=1{t.Fatalf("unexpected generation count %d %v",count,err)}
}
