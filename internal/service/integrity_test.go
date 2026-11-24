package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ynw0/airgap-mirror/internal/adapters/common"
	"github.com/ynw0/airgap-mirror/internal/domain"
	storesqlite "github.com/ynw0/airgap-mirror/internal/storage/sqlite"
)

func integrityDigest(s string)string{
	h:=sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestIntegrityResumeReportsSameSizeCorruptionWithoutChangingCatalog(t *testing.T){
	ctx:=context.Background()
	db,err:=sql.Open("sqlite",filepath.Join(t.TempDir(),"server.db"))
	if err!=nil{t.Fatal(err)}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err=storesqlite.InitServer(ctx,db);err!=nil{t.Fatal(err)}
	store:=storesqlite.NewServerStore(db)
	id,err:=domain.NewID()
	if err!=nil{t.Fatal(err)}
	now:=time.Now()
	src:=domain.Source{ID:id,Name:"PyPI",Type:domain.SourcePyPI,Provider:"pypi",UpstreamURL:"https://pypi.org/simple",PublicURL:"https://repo.example/pypi",RootPath:t.TempDir(),ConfigJSON:json.RawMessage("{}"),Enabled:true,CreatedAt:now,UpdatedAt:now}
	if err=store.CreateSource(ctx,src);err!=nil{t.Fatal(err)}
	if err=os.MkdirAll(filepath.Join(src.RootPath,"packages"),0755);err!=nil{t.Fatal(err)}
	for _,v:=range []struct{name,actual string}{
		{"a","good"},{"b","evil"},{"c",""},{"d","shorter?"},{"e","good"},
	}{
		logical:="packages/"+v.name+".whl"
		if v.actual!=""{
			if err=os.WriteFile(filepath.Join(src.RootPath,filepath.FromSlash(logical)),[]byte(v.actual),0644);err!=nil{t.Fatal(err)}
		}
		if err=store.Upsert(ctx,domain.CatalogEntry{SourceID:id,LogicalPath:logical,Size:4,SHA256:integrityDigest("good"),PackageKey:"demo",UpdatedAt:now});err!=nil{t.Fatal(err)}
	}
	state,err:=store.GetState(ctx,id)
	if err!=nil{t.Fatal(err)}
	state.LiveObjects=5
	state.LiveBytes=20
	state.LiveCursor=domain.Cursor{Kind:"pypi-serial",Value:"123"}
	if err=store.PutState(ctx,state);err!=nil{t.Fatal(err)}
	jobID,err:=domain.NewID()
	if err!=nil{t.Fatal(err)}
	job:=domain.MaintenanceJob{ID:jobID,SourceID:id,Kind:domain.MaintenanceRepair,Status:domain.MaintenanceQueued,HashMiBPerSecond:64,CreatedAt:now}
	if err=store.CreateMaintenanceJob(ctx,job);err!=nil{t.Fatal(err)}
	if err=store.TransitionMaintenanceJob(ctx,jobID,domain.MaintenanceRunning,"",now);err!=nil{t.Fatal(err)}
	if _,err=store.BeginRepairScan(ctx,jobID,id);err!=nil{t.Fatal(err)}
	// First file was verified and committed before the simulated restart.
	if err=store.CommitRepairPage(ctx,jobID,"","packages/a.whl",1,4,nil);err!=nil{t.Fatal(err)}
	if err=store.RecoverInterruptedMaintenanceJobs(ctx,now,"restart");err!=nil{t.Fatal(err)}
	job,err=store.GetMaintenanceJob(ctx,jobID)
	if err!=nil||job.HashMiBPerSecond!=64||job.Status!=domain.MaintenancePaused||job.ScannedObjects!=1{
		t.Fatalf("mode or checkpoint lost: %+v %v",job,err)
	}
	if err=store.TransitionMaintenanceJob(ctx,jobID,domain.MaintenanceRunning,"",now);err!=nil{t.Fatal(err)}
	runner:=NewMaintenanceRunner(ctx,store,store,store,nil,nil)
	if err=runner.scanRepair(ctx,store,job,src);err!=nil{t.Fatal(err)}
	done,err:=store.GetMaintenanceJob(ctx,jobID)
	if err!=nil||done.Status!=domain.MaintenanceCompleted||done.ScannedObjects!=5||done.CandidateObjects!=3{t.Fatalf("unexpected completion %+v %v",done,err)}
	issues,err:=store.ListRepairIssues(ctx,jobID,"",100)
	if err!=nil||len(issues)!=3{t.Fatalf("issues %+v %v",issues,err)}
	if issues[0].Kind!="HASH_MISMATCH"||issues[0].ActualSHA256!=integrityDigest("evil")||issues[0].SHA256!=integrityDigest("good")||issues[1].Kind!="MISSING"||issues[2].Kind!="SIZE_MISMATCH"{
		t.Fatalf("wrong issue classification: %+v",issues)
	}
	entry,err:=store.Get(ctx,id,"packages/b.whl")
	if err!=nil||entry.SHA256!=integrityDigest("good"){t.Fatal("live catalog was modified")}
	after,err:=store.GetState(ctx,id)
	if err!=nil||!after.LiveCursor.Equal(state.LiveCursor)||after.CatalogVersion!=state.CatalogVersion{t.Fatal("live cursor or version modified")}
	body,err:=os.ReadFile(filepath.Join(src.RootPath,"packages","b.whl"))
	if err!=nil||string(body)!="evil"{t.Fatal("corrupt file was modified during verification")}
	// A repair export may omit only the missing file, never corrupt files.
	path:=filepath.Join(t.TempDir(),"repair.sqlite")
	if _,_,err=store.ExportRepairSnapshot(ctx,id,path,jobID);err!=nil{t.Fatal(err)}
	sdb,snap,err:=storesqlite.OpenCatalogSnapshot(path)
	if err!=nil{t.Fatal(err)}
	defer sdb.Close()
	if _,err=snap.Get(ctx,id,"packages/b.whl");err!=nil{t.Fatal("corrupt file incorrectly treated as missing")}
	if _,err=snap.Get(ctx,id,"packages/c.whl");!errors.Is(err,domain.ErrNotFound){t.Fatal("missing file not omitted")}
}

func TestHashRepositoryFileCancellation(t *testing.T){
	root:=t.TempDir()
	path:=filepath.Join(root,"data")
	if err:=os.WriteFile(path,make([]byte,1024*1024),0644);err!=nil{t.Fatal(err)}
	repo,err:=common.OpenLocalRepository(root)
	if err!=nil{t.Fatal(err)}
	_,info,err:=repo.Resolve("data")
	if err!=nil{t.Fatal(err)}
	ctx,cancel:=context.WithCancel(context.Background())
	cancel()
	if _,err=hashRepositoryFile(ctx,repo,"data",info,1,make([]byte,256*1024));!errors.Is(err,context.Canceled){t.Fatalf("cancel ignored: %v",err)}
}
