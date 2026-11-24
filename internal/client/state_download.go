package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type stateDownloadCheckpoint struct {
	Name string
	ETag string
	Size int64
}

// DownloadState retains an incomplete download on disk. It can only resume
// when the server proves that the exported file has the same ETag and length.
func (c *AgentClient) DownloadState(ctx context.Context, name,destination string)error{
	if name==""||filepath.Base(name)!=name||strings.ContainsAny(name,"/\\")||destination==""{return domain.ErrInvalid}
	if _,err:=os.Stat(destination);err==nil{return fmt.Errorf("目标状态文件已经存在: %w",domain.ErrConflict)}else if !errors.Is(err,os.ErrNotExist){return err}
	requestPath:="/api/v1/state/exports/"+url.PathEscape(name)
	req,err:=c.newRequest(ctx,http.MethodHead,requestPath,nil)
	if err!=nil{return err}
	resp,err:=c.do(req)
	if err!=nil{return err}
	etag,size:=resp.Header.Get("ETag"),resp.ContentLength
	_ = resp.Body.Close()
	if etag==""||strings.HasPrefix(etag,"W/")||size<0{return c.downloadStateLegacy(ctx,name,destination)}
	if err=os.MkdirAll(filepath.Dir(destination),0755);err!=nil{return err}
	partial:=destination+".part"
	meta:=partial+".json"
	expected:=stateDownloadCheckpoint{Name:name,ETag:etag,Size:size}
	var saved stateDownloadCheckpoint
	data,err:=os.ReadFile(meta)
	if err==nil{
		if json.Unmarshal(data,&saved)!=nil||saved.Name!=name{
			return fmt.Errorf("目标存在另一个下载的检查点，请选择其他目标: %w",domain.ErrConflict)
		}
		if saved!=expected{
			return fmt.Errorf("服务器状态文件已变化，请选择新的目标或手动处理旧的 .part 文件: %w",domain.ErrConflict)
		}
	}else if errors.Is(err,os.ErrNotExist){
		if _,err=os.Lstat(partial);!errors.Is(err,os.ErrNotExist){return fmt.Errorf("目标存在无法识别的 .part 文件: %w",domain.ErrConflict)}
		data,err=json.Marshal(expected)
		if err!=nil{return err}
		m,e:=os.OpenFile(meta,os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600)
		if e!=nil{return e}
		_,e=m.Write(data)
		if e==nil{e=m.Sync()}
		closeErr:=m.Close()
		if e!=nil{return e}
		if closeErr!=nil{return closeErr}
	}else{return err}
	if info,e:=os.Lstat(partial);e==nil&&!info.Mode().IsRegular(){return domain.ErrInvalid}else if e!=nil&&!errors.Is(e,os.ErrNotExist){return e}
	f,err:=os.OpenFile(partial,os.O_CREATE|os.O_WRONLY,0600)
	if err!=nil{return err}
	defer f.Close()
	info,err:=f.Stat()
	if err!=nil{return err}
	offset:=info.Size()
	if offset>size{return fmt.Errorf("本地下载文件超过服务器长度: %w",domain.ErrConflict)}
	if offset<size{
		req,err=c.newRequest(ctx,http.MethodGet,requestPath,nil)
		if err!=nil{return err}
		req.Header.Set("If-Match",etag)
		if offset>0{
			req.Header.Set("Range",fmt.Sprintf("bytes=%d-",offset))
			req.Header.Set("If-Range",etag)
		}
		resp,err=c.do(req)
		if err!=nil{return err}
		defer resp.Body.Close()
		if resp.Header.Get("ETag")!=etag{return fmt.Errorf("下载文件标识已改变: %w",domain.ErrConflict)}
		if offset>0 {
			want:=fmt.Sprintf("bytes %d-%d/%d",offset,size-1,size)
			if resp.StatusCode!=http.StatusPartialContent||resp.Header.Get("Content-Range")!=want||resp.ContentLength!=size-offset{
				return fmt.Errorf("服务器未返回正确的断点范围: %w",domain.ErrConflict)
			}
		}else if resp.StatusCode!=http.StatusOK||resp.ContentLength!=size{return domain.ErrConflict}
		if _,err=f.Seek(offset,io.SeekStart);err!=nil{return err}
		n,copyErr:=io.Copy(f,io.LimitReader(resp.Body,size-offset))
		syncErr:=f.Sync()
		if copyErr!=nil{return copyErr}
		if syncErr!=nil{return syncErr}
		if n!=size-offset{return io.ErrUnexpectedEOF}
	}
	if err=f.Sync();err!=nil{return err}
	if err=f.Close();err!=nil{return err}
	if _,err=os.Stat(destination);err==nil{return domain.ErrConflict}else if !errors.Is(err,os.ErrNotExist){return err}
	if err=os.Rename(partial,destination);err!=nil{return err}
	_ = os.Remove(meta)
	return nil
}
