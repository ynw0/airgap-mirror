package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

const exportJobSelect = `SELECT id,source_ids,repair_job_id,status,stage,completed_sources,total_sources,result_json,error_text,created_at,updated_at FROM state_export_jobs`

func scanExportJob(row interface{Scan(...any)error})(domain.ExportJob,error){
	var j domain.ExportJob
	var ids,result []byte
	var created,updated string
	err:=row.Scan(&j.ID,&ids,&j.RepairJobID,&j.Status,&j.Stage,&j.CompletedSources,&j.TotalSources,&result,&j.ErrorText,&created,&updated)
	if err!=nil{return j,mapNotFound(err)}
	if err=json.Unmarshal(ids,&j.SourceIDs);err!=nil{return j,err}
	if len(result)>0{
		var cap domain.StateCapsule
		if err=json.Unmarshal(result,&cap);err!=nil{return j,err}
		j.Capsule=&cap
		j.Name="mirror-state-"+j.ID+".mstate"
		j.Download="/api/v1/state/exports/"+j.Name
	}
	j.CreatedAt=parseTime(created)
	j.UpdatedAt=parseTime(updated)
	return j,nil
}
func(s *ServerStore) CreateExportJob(ctx context.Context,j domain.ExportJob)error{
	ids,err:=json.Marshal(j.SourceIDs)
	if err!=nil{return err}
	tx,err:=s.DB.BeginTx(ctx,nil)
	if err!=nil{return err}
	defer tx.Rollback()
	var pending int
	if err=tx.QueryRowContext(ctx,"SELECT COUNT(*) FROM state_export_jobs WHERE status IN('QUEUED','RUNNING')").Scan(&pending);err!=nil{return err}
	if pending>=20{return fmt.Errorf("状态文件导出队列已满: %w",domain.ErrConflict)}
	_,err=tx.ExecContext(ctx,`INSERT INTO state_export_jobs(id,source_ids,repair_job_id,status,stage,total_sources,created_at,updated_at) VALUES(?,?,?,'QUEUED','等待开始',?,?,?)`,j.ID,ids,j.RepairJobID,len(j.SourceIDs),timeString(j.CreatedAt),timeString(j.UpdatedAt))
	if err!=nil{return err}
	return tx.Commit()
}
func(s *ServerStore) GetExportJob(ctx context.Context,id string)(domain.ExportJob,error){
	return scanExportJob(s.DB.QueryRowContext(ctx,exportJobSelect+" WHERE id=?",id))
}
func(s *ServerStore) ListExportJobs(ctx context.Context)([]domain.ExportJob,error){
	rows,err:=s.DB.QueryContext(ctx,exportJobSelect+" ORDER BY created_at DESC LIMIT 50")
	if err!=nil{return nil,err}
	defer rows.Close()
	out:=make([]domain.ExportJob,0)
	for rows.Next(){j,e:=scanExportJob(rows);if e!=nil{return nil,e};out=append(out,j)}
	return out,rows.Err()
}
func(s *ServerStore) RecoverExportJobs(ctx context.Context)error{
	_,err:=s.DB.ExecContext(ctx,`UPDATE state_export_jobs SET status='QUEUED',stage='Agent 重启，等待重新生成',completed_sources=0,error_text='',updated_at=? WHERE status='RUNNING'`,timeString(time.Now()))
	return err
}
func(s *ServerStore) ClaimExportJob(ctx context.Context)(domain.ExportJob,error){
	tx,err:=s.DB.BeginTx(ctx,nil)
	if err!=nil{return domain.ExportJob{},err}
	defer tx.Rollback()
	j,err:=scanExportJob(tx.QueryRowContext(ctx,exportJobSelect+" WHERE status='QUEUED' ORDER BY created_at,id LIMIT 1"))
	if err!=nil{return j,err}
	res,err:=tx.ExecContext(ctx,`UPDATE state_export_jobs SET status='RUNNING',stage='准备导出',updated_at=? WHERE id=? AND status='QUEUED'`,timeString(time.Now()),j.ID)
	if err!=nil{return j,err}
	if n,_:=res.RowsAffected();n!=1{return j,domain.ErrConflict}
	j.Status="RUNNING"
	return j,tx.Commit()
}
func(s *ServerStore) UpdateExportJob(ctx context.Context,id,stage string,done int)error{
	res,err:=s.DB.ExecContext(ctx,`UPDATE state_export_jobs SET stage=?,completed_sources=?,updated_at=? WHERE id=? AND status='RUNNING' AND completed_sources<=? AND total_sources>=?`,stage,done,timeString(time.Now()),id,done,done)
	if err!=nil{return err}
	if n,_:=res.RowsAffected();n!=1{return domain.ErrConflict}
	return nil
}
func(s *ServerStore) FinishExportJob(ctx context.Context,id string,cap *domain.StateCapsule,message string)error{
	status,stage:="FAILED","导出失败"
	var result []byte
	if cap!=nil{
		if cap.ID!=id{return domain.ErrConflict}
		var err error
		result,err=json.Marshal(cap)
		if err!=nil{return err}
		status,stage="COMPLETED","可以下载"
	}
	res,err:=s.DB.ExecContext(ctx,`UPDATE state_export_jobs SET status=?,stage=?,result_json=?,error_text=?,updated_at=? WHERE id=? AND status='RUNNING'`,status,stage,result,message,timeString(time.Now()),id)
	if err!=nil{return err}
	if n,_:=res.RowsAffected();n!=1{return domain.ErrConflict}
	return nil
}
