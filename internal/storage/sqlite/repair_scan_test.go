package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestRepairReportCheckpointAndExportDoNotModifyLiveCatalog(t *testing.T){
	ctx:=context.Background()
	_,store:=openResumableTestDB(t)
	src:=fixturePyPI(t,store,1)
	now:=time.Now()
	entries:=[]domain.CatalogEntry{
		{SourceID:src.ID,LogicalPath:"packages/missing.whl",PackageKey:"missing",Size:5,SHA256:shaOf("wheel"),UpdatedAt:now},
		{SourceID:src.ID,LogicalPath:"packages/wrong-size.whl",PackageKey:"wrong-size",Size:10,SHA256:shaOf("incorrect"),UpdatedAt:now},
	}
	for _,entry:=range entries{if err:=store.Upsert(ctx,entry);err!=nil{t.Fatal(err)}}
	state,err:=store.GetState(ctx,src.ID)
	if err!=nil{t.Fatal(err)}
	state.LiveObjects=2
	state.LiveBytes=15
	state.LiveCursor=domain.Cursor{Kind:"pypi-serial",Value:"5"}
	if err=store.PutState(ctx,state);err!=nil{t.Fatal(err)}
	job:=domain.MaintenanceJob{ID:mustID(t),SourceID:src.ID,Kind:domain.MaintenanceRepair,Status:domain.MaintenanceQueued,CreatedAt:now}
	if err=store.CreateMaintenanceJob(ctx,job);err!=nil{t.Fatal(err)}
	if err=store.TransitionMaintenanceJob(ctx,job.ID,domain.MaintenanceRunning,"",now);err!=nil{t.Fatal(err)}
	cp,err:=store.BeginRepairScan(ctx,job.ID,src.ID)
	if err!=nil||cp.LastPath!=""{t.Fatalf("initial checkpoint: %+v %v",cp,err)}
	page,err:=store.RepairCatalogPage(ctx,src.ID,"",1)
	if err!=nil||len(page)!=1||page[0].LogicalPath!=entries[0].LogicalPath{t.Fatalf("first page: %+v %v",page,err)}
	missing:=domain.RepairIssue{LogicalPath:entries[0].LogicalPath,PackageKey:entries[0].PackageKey,Size:5,SHA256:entries[0].SHA256,Kind:"MISSING",ActualSize:-1}
	if err=store.CommitRepairPage(ctx,job.ID,"",missing.LogicalPath,1,5,[]domain.RepairIssue{missing});err!=nil{t.Fatal(err)}
	if err=store.RecoverInterruptedMaintenanceJobs(ctx,now,"restart");err!=nil{t.Fatal(err)}
	paused,err:=store.GetMaintenanceJob(ctx,job.ID)
	if err!=nil||paused.Status!=domain.MaintenancePaused||paused.ScannedObjects!=1{t.Fatalf("recovery: %+v %v",paused,err)}
	if err=store.TransitionMaintenanceJob(ctx,job.ID,domain.MaintenanceRunning,"",now);err!=nil{t.Fatal(err)}
	cp,err=store.BeginRepairScan(ctx,job.ID,src.ID)
	if err!=nil||cp.LastPath!=missing.LogicalPath{t.Fatalf("resume checkpoint: %+v %v",cp,err)}
	wrong:=domain.RepairIssue{LogicalPath:entries[1].LogicalPath,PackageKey:entries[1].PackageKey,Size:10,SHA256:entries[1].SHA256,Kind:"SIZE_MISMATCH",ActualSize:2}
	if err=store.CommitRepairPage(ctx,job.ID,cp.LastPath,wrong.LogicalPath,1,10,[]domain.RepairIssue{wrong});err!=nil{t.Fatal(err)}
	if err=store.FinishRepairScan(ctx,job.ID,now);err!=nil{t.Fatal(err)}
	dest:=filepath.Join(t.TempDir(),"repair.sqlite")
	_,exported,err:=store.ExportRepairSnapshot(ctx,src.ID,dest,job.ID)
	if err!=nil||!exported.LiveCursor.Equal(state.LiveCursor){t.Fatalf("repair export: %+v %v",exported,err)}
	db,snap,err:=OpenCatalogSnapshot(dest)
	if err!=nil{t.Fatal(err)}
	defer db.Close()
	if err=snap.ValidateRepairSnapshot(ctx,src.ID,true);err!=nil{t.Fatal(err)}
	if _,err=snap.Get(ctx,src.ID,missing.LogicalPath);!errors.Is(err,domain.ErrNotFound){t.Fatal("missing file still classified as present in repair capsule")}
	if _,err=snap.Get(ctx,src.ID,wrong.LogicalPath);err!=nil{t.Fatal("incorrect-size file must not be omitted")}
	var projects []string
	if err=snap.WalkRepairProjects(ctx,src.ID,func(project string)error{projects=append(projects,project);return nil});err!=nil||len(projects)!=1||projects[0]!="missing"{t.Fatalf("wrong repair projects: %v %v",projects,err)}
	if _,err=store.Get(ctx,src.ID,missing.LogicalPath);err!=nil{t.Fatal("live catalog changed by report/export")}
	state.CatalogVersion++
	if err=store.PutState(ctx,state);err!=nil{t.Fatal(err)}
	if _,_,err=store.ExportRepairSnapshot(ctx,src.ID,filepath.Join(t.TempDir(),"stale.sqlite"),job.ID);!errors.Is(err,domain.ErrConflict){t.Fatalf("stale report exported: %v",err)}
}
