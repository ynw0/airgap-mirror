package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestRepairApprovalOnlyAuthorizesCurrentCatalogAndVerifiedPackages(t *testing.T){
	ctx:=context.Background()
	_,s:=openResumableTestDB(t)
	src:=fixturePyPI(t,s,1)
	now:=time.Now()
	path:="packages/a.whl"
	hash:=shaOf("good")
	for _,v:=range []domain.CatalogEntry{
		{SourceID:src.ID,LogicalPath:path,PackageKey:"a",Size:4,SHA256:hash,UpdatedAt:now},
		{SourceID:src.ID,LogicalPath:"simple/a/index.html",PackageKey:"a",Size:4,SHA256:hash,UpdatedAt:now},
		{SourceID:src.ID,LogicalPath:"packages/b.whl",PackageKey:"b",Size:4,SHA256:hash,UpdatedAt:now},
	}{if err:=s.Upsert(ctx,v);err!=nil{t.Fatal(err)}}
	job:=domain.MaintenanceJob{ID:mustID(t),SourceID:src.ID,Kind:domain.MaintenanceRepair,Status:domain.MaintenanceQueued,HashMiBPerSecond:64,CreatedAt:now}
	if err:=s.CreateMaintenanceJob(ctx,job);err!=nil{t.Fatal(err)}
	if err:=s.TransitionMaintenanceJob(ctx,job.ID,domain.MaintenanceRunning,"",now);err!=nil{t.Fatal(err)}
	if _,err:=s.BeginRepairScan(ctx,job.ID,src.ID);err!=nil{t.Fatal(err)}
	issues:=[]domain.RepairIssue{
		{LogicalPath:path,PackageKey:"a",Size:4,SHA256:hash,Kind:"HASH_MISMATCH",ActualSize:4,ActualSHA256:shaOf("evil")},
		{LogicalPath:"packages/b.whl",PackageKey:"b",Size:4,SHA256:hash,Kind:"SIZE_MISMATCH",ActualSize:3},
		{LogicalPath:"simple/a/index.html",PackageKey:"a",Size:4,SHA256:hash,Kind:"HASH_MISMATCH",ActualSize:4,ActualSHA256:shaOf("evil")},
	}
	if err:=s.CommitRepairPage(ctx,job.ID,"","simple/a/index.html",3,12,issues);err!=nil{t.Fatal(err)}
	if err:=s.ApproveRepairReplacement(ctx,job.ID,true);!errors.Is(err,domain.ErrConflict){t.Fatalf("running report approved: %v",err)}
	if err:=s.FinishRepairScan(ctx,job.ID,now);err!=nil{t.Fatal(err)}
	if _,_,err:=s.ExportRepairSnapshot(ctx,src.ID,filepath.Join(t.TempDir(),"not-approved.sqlite"),job.ID);!errors.Is(err,domain.ErrConflict){t.Fatalf("unapproved damage exported: %v",err)}
	if err:=s.ApproveRepairReplacement(ctx,job.ID,true);err!=nil{t.Fatal(err)}
	j,err:=s.GetMaintenanceJob(ctx,job.ID)
	if err!=nil||!j.ReplacementApproved{t.Fatalf("approval not visible: %+v %v",j,err)}
	dest:=filepath.Join(t.TempDir(),"approved.sqlite")
	if _,_,err=s.ExportRepairSnapshot(ctx,src.ID,dest,job.ID);err!=nil{t.Fatal(err)}
	db,snap,err:=OpenCatalogSnapshot(dest)
	if err!=nil{t.Fatal(err)}
	defer db.Close()
	if _,err=snap.Get(ctx,src.ID,path);!errors.Is(err,domain.ErrNotFound){t.Fatal("approved corrupt file not included in repair work")}
	for _,p:=range []string{"packages/b.whl","simple/a/index.html"}{
		if _,err=snap.Get(ctx,src.ID,p);err!=nil{t.Fatalf("ineligible file %s omitted: %v",p,err)}
	}
	if err=s.ApproveRepairReplacement(ctx,job.ID,false);err!=nil{t.Fatal(err)}
	if err=s.ApproveRepairReplacement(ctx,job.ID,true);err!=nil{t.Fatal(err)}
	state,err:=s.GetState(ctx,src.ID)
	if err!=nil{t.Fatal(err)}
	state.ActiveEpochID="test-epoch"
	if err=s.PutState(ctx,state);err!=nil{t.Fatal(err)}
	e,err:=s.ApprovedRepairEvidence(ctx,src.ID,state.ActiveEpochID,path,4,hash)
	if err!=nil||e.ActualSHA256!=shaOf("evil"){t.Fatalf("wrong repair evidence: %+v %v",e,err)}
	if _,err=s.ApprovedRepairEvidence(ctx,src.ID,state.ActiveEpochID,path,4,shaOf("fake"));!errors.Is(err,domain.ErrNotFound){t.Fatalf("different hash authorized: %v",err)}
	if _,err=s.ApprovedRepairEvidence(ctx,src.ID,"other-epoch",path,4,hash);!errors.Is(err,domain.ErrNotFound){t.Fatalf("different epoch authorized: %v",err)}
	if err=s.ApproveRepairReplacement(ctx,job.ID,false);!errors.Is(err,domain.ErrConflict){t.Fatalf("authorization revoked during active import: %v",err)}
	state.ActiveEpochID=""
	state.CatalogVersion++
	if err=s.PutState(ctx,state);err!=nil{t.Fatal(err)}
	j,err=s.GetMaintenanceJob(ctx,job.ID)
	if err!=nil||j.ReplacementApproved{t.Fatalf("stale authorization still valid: %+v %v",j,err)}
	if err=s.ApproveRepairReplacement(ctx,job.ID,true);!errors.Is(err,domain.ErrConflict){t.Fatalf("stale report approved: %v",err)}
}
