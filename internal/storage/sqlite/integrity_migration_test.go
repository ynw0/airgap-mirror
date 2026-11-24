package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestIntegrityMigrationPreservesLegacyRepairReports(t *testing.T){
	ctx:=context.Background()
	db,err:=sql.Open("sqlite",filepath.Join(t.TempDir(),"legacy.db"))
	if err!=nil{t.Fatal(err)}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err=initSchema(ctx,db,serverSchema);err!=nil{t.Fatal(err)}
	legacy:=strings.ReplaceAll(maintenanceSchema,"  hash_mib_per_second INTEGER NOT NULL DEFAULT 0 CHECK(hash_mib_per_second BETWEEN 0 AND 1024),\n","")
	legacy=strings.ReplaceAll(legacy,"'MISSING','SIZE_MISMATCH','HASH_MISMATCH'","'MISSING','SIZE_MISMATCH'")
	legacy=strings.ReplaceAll(legacy," actual_sha256 TEXT NOT NULL DEFAULT '',\n","")
	if err=initSchema(ctx,db,legacy);err!=nil{t.Fatal(err)}
	store:=NewServerStore(db)
	src:=fixturePyPI(t,store,1)
	now:=timeString(time.Now())
	if _,err=db.ExecContext(ctx,`INSERT INTO maintenance_jobs(id,source_id,kind,status,execute,created_at) VALUES('legacy-repair',?,'REPAIR','COMPLETED',0,?)`,src.ID,now);err!=nil{t.Fatal(err)}
	if _,err=db.ExecContext(ctx,`INSERT INTO repair_scans(job_id,catalog_version,source_updated_at) VALUES('legacy-repair',0,?)`,timeString(src.UpdatedAt));err!=nil{t.Fatal(err)}
	if _,err=db.ExecContext(ctx,`INSERT INTO repair_issues(job_id,logical_path,package_key,size,sha256,kind,actual_size) VALUES('legacy-repair','packages/a.whl','a',4,?,'MISSING',-1)`,shaOf("good"));err!=nil{t.Fatal(err)}
	if err=InitServer(ctx,db);err!=nil{t.Fatal(err)}
	job,err:=store.GetMaintenanceJob(ctx,"legacy-repair")
	if err!=nil||job.Status!=domain.MaintenanceCompleted||job.HashMiBPerSecond!=0{t.Fatalf("old job changed: %+v %v",job,err)}
	issues,err:=store.ListRepairIssues(ctx,"legacy-repair","",100)
	if err!=nil||len(issues)!=1||issues[0].Kind!="MISSING"||issues[0].ActualSHA256!=""{t.Fatalf("old report changed: %+v %v",issues,err)}
	if err=InitServer(ctx,db);err!=nil{t.Fatal("migration not idempotent",err)}
}
