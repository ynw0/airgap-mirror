package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
)

func safeTarget(root, logical string) (string, error) {
	if err := pack.ValidateLogicalPath(logical); err != nil { return "", err }
	absRoot, err := filepath.Abs(root); if err != nil { return "", err }
	target := filepath.Join(absRoot, filepath.FromSlash(logical))
	rel, err := filepath.Rel(absRoot, target); if err != nil { return "", err }
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) { return "", fmt.Errorf("logical path escapes source root: %w", domain.ErrInvalid) }
	return target,nil
}
func rejectSymlinkParents(root,target string)error{
	root,err:=filepath.Abs(root);if err!=nil{return err};target,err=filepath.Abs(target);if err!=nil{return err}
	info,err:=os.Lstat(root);if err!=nil{return err};if info.Mode()&os.ModeSymlink!=0{return fmt.Errorf("source root may not be symlink")}
	dir:=filepath.Dir(target);rel,err:=filepath.Rel(root,dir);if err!=nil{return err};if rel=="."{return nil};cur:=root
	for _,part:=range strings.Split(rel,string(filepath.Separator)){cur=filepath.Join(cur,part);info,err=os.Lstat(cur);if errors.Is(err,os.ErrNotExist){continue};if err!=nil{return err};if info.Mode()&os.ModeSymlink!=0{return fmt.Errorf("symlink path component %s rejected",cur)}}
	return nil
}
func hashFile(path string)(string,int64,error){f,err:=os.Open(path);if err!=nil{return "",0,err};defer f.Close();h:=sha256.New();n,err:=io.Copy(h,f);if err!=nil{return "",n,err};return hex.EncodeToString(h.Sum(nil)),n,nil}
func writeVerified(root,target string,size int64,want string,r io.Reader,replace bool)(bool,error){
	if err:=rejectSymlinkParents(root,target);err!=nil{return false,err}
	if info,err:=os.Stat(target);err==nil{h,n,e:=hashFile(target);if e!=nil{return false,e};if info.Size()==size&&n==size&&h==want{return true,nil};if !replace{return false,fmt.Errorf("target %s differs from incoming content: %w",target,domain.ErrConflict)}}else if !errors.Is(err,os.ErrNotExist){return false,err}
	if err:=os.MkdirAll(filepath.Dir(target),0755);err!=nil{return false,err};if err:=rejectSymlinkParents(root,target);err!=nil{return false,err}
	tmp,err:=os.CreateTemp(filepath.Dir(target),".airgap-write-*");if err!=nil{return false,err};tmpName:=tmp.Name();ok:=false;defer func(){tmp.Close();if !ok{_ = os.Remove(tmpName)}}()
	h:=sha256.New();n,err:=io.Copy(io.MultiWriter(tmp,h),io.LimitReader(r,size+1));if err!=nil{return false,err};if n!=size{return false,fmt.Errorf("content size %d != expected %d",n,size)};got:=hex.EncodeToString(h.Sum(nil));if got!=want{return false,fmt.Errorf("content sha256 %s != %s",got,want)};if err=tmp.Sync();err!=nil{return false,err};if err=tmp.Close();err!=nil{return false,err}
	if replace{if err=os.Rename(tmpName,target);err!=nil{return false,err};ok=true}else{if err=os.Link(tmpName,target);err!=nil{if errors.Is(err,os.ErrExist){return false,fmt.Errorf("target appeared during install: %w",domain.ErrConflict)};return false,err};if err=os.Remove(tmpName);err!=nil{return false,err};ok=true}
	if d,e:=os.Open(filepath.Dir(target));e==nil{_ = d.Sync();_ = d.Close()};return false,nil
}
