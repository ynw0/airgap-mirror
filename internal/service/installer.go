package service

import (
	"context"
	"io"
	"github.com/ynw0/airgap-mirror/internal/domain"
)

type FileInstaller struct{}
func (FileInstaller) Install(ctx context.Context, source domain.Source, record domain.PackRecord, r io.Reader)(domain.InstallResult,error){
	_ = ctx
	target,err:=safeTarget(source.RootPath,record.LogicalPath); if err!=nil{return domain.InstallResult{},err}
	skipped,err:=writeVerified(source.RootPath,target,record.ContentLength,record.SHA256,r,false); if err!=nil{return domain.InstallResult{},err}
	return domain.InstallResult{LogicalPath:record.LogicalPath,Bytes:record.ContentLength,Skipped:skipped},nil
}
