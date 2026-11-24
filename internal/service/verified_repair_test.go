package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestVerifiedRepairPreservesOriginalUntilPayloadVerified(t *testing.T) {
	ctx:=context.Background()
	root:=t.TempDir()
	src:=domain.Source{Type:domain.SourcePyPI,Provider:"pypi",RootPath:root}
	logical:="packages/demo.whl"
	target:=filepath.Join(root,filepath.FromSlash(logical))
	if err:=os.MkdirAll(filepath.Dir(target),0755);err!=nil{t.Fatal(err)}
	if err:=os.WriteFile(target,[]byte("evil"),0644);err!=nil{t.Fatal(err)}
	rec:=domain.PackRecord{LogicalPath:logical,ContentLength:4,SHA256:shaHex("good")}
	evidence:=domain.RepairReplacementEvidence{JobID:"job-1",LogicalPath:logical,Size:4,SHA256:rec.SHA256,ActualSize:4,ActualSHA256:shaHex("evil")}
	// Normal immutable imports must continue to reject a different file.
	if _,err:=(FileInstaller{}).Install(ctx,src,rec,bytes.NewReader([]byte("good")));!errors.Is(err,domain.ErrConflict){t.Fatalf("ordinary import overwrites corruption: %v",err)}
	for _,test:=range []struct{name,payload string; evidence domain.RepairReplacementEvidence}{
		{"bad payload","nope",evidence},
		{"truncated payload","go",evidence},
		{"changed original","good",domain.RepairReplacementEvidence{JobID:"job-1",LogicalPath:logical,Size:4,SHA256:rec.SHA256,ActualSize:4,ActualSHA256:shaHex("old!")}},
		{"different trusted hash","good",domain.RepairReplacementEvidence{JobID:"job-1",LogicalPath:logical,Size:4,SHA256:shaHex("fake"),ActualSize:4,ActualSHA256:shaHex("evil")}},
	}{
		t.Run(test.name,func(t *testing.T){
			if _,err:=repairVerifiedPackage(ctx,src,rec,bytes.NewReader([]byte(test.payload)),test.evidence);err==nil{t.Fatal("invalid repair succeeded")}
			got,err:=os.ReadFile(target)
			if err!=nil||string(got)!="evil"{t.Fatalf("original was modified: %q %v",got,err)}
			if _,err=os.Stat(filepath.Join(root,".airgap-staging","repair-quarantine"));!os.IsNotExist(err){t.Fatalf("quarantine created before validation: %v",err)}
		})
	}
	result,err:=repairVerifiedPackage(ctx,src,rec,bytes.NewReader([]byte("good")),evidence)
	if err!=nil||result.Skipped{t.Fatalf("repair: %+v %v",result,err)}
	got,err:=os.ReadFile(target)
	if err!=nil||string(got)!="good"{t.Fatalf("repaired content: %q %v",got,err)}
	quarantine:=filepath.Join(root,".airgap-staging","repair-quarantine")
	info,err:=os.Stat(quarantine)
	if err!=nil||info.Mode().Perm()!=0700{t.Fatalf("quarantine permissions: %v %v",info,err)}
	backups,err:=filepath.Glob(filepath.Join(quarantine,"job-1","*","packages","demo.whl"))
	if err!=nil||len(backups)!=1{t.Fatalf("wrong backups: %v %v",backups,err)}
	old,err:=os.ReadFile(backups[0])
	if err!=nil||string(old)!="evil"{t.Fatalf("old data not preserved: %q %v",old,err)}
	result,err=repairVerifiedPackage(ctx,src,rec,bytes.NewReader([]byte("good")),evidence)
	if err!=nil||!result.Skipped{t.Fatalf("idempotent retry failed: %+v %v",result,err)}
	backups,err=filepath.Glob(filepath.Join(quarantine,"job-1","*","packages","demo.whl"))
	if err!=nil||len(backups)!=1{t.Fatalf("retry made another backup: %v %v",backups,err)}
}
