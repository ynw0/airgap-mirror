package client

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestStateDownloadResumesUsingRangeAndETag(t *testing.T){
	payload:=[]byte("a durable state capsule fixture")
	var gets atomic.Int32
	var rangeOK atomic.Bool
	const etag = "\"export-1\""
	server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		if r.Header.Get("Authorization")!="Bearer test"{w.WriteHeader(401);return}
		w.Header().Set("ETag",etag)
		w.Header().Set("Content-Length",fmt.Sprint(len(payload)))
		if r.Method==http.MethodGet&&gets.Add(1)==1{
			w.WriteHeader(http.StatusOK)
			_,_=w.Write(payload[:8])
			return // truncated response, Content-Length remains full length
		}
		if r.Method==http.MethodGet&&r.Header.Get("Range")=="bytes=8-"&&r.Header.Get("If-Range")==etag&&r.Header.Get("If-Match")==etag{rangeOK.Store(true)}
		http.ServeContent(w,r,"mirror-state-test.mstate",time.Unix(100,0),bytes.NewReader(payload))
	}))
	defer server.Close()
	c,err:=NewAgent(server.URL,"test",server.Client())
	if err!=nil{t.Fatal(err)}
	dest:=filepath.Join(t.TempDir(),"state.mstate")
	if err=c.DownloadState(context.Background(),"mirror-state-test.mstate",dest);err==nil{t.Fatal("truncated transfer succeeded")}
	if b,e:=os.ReadFile(dest+".part");e!=nil||!bytes.Equal(b,payload[:8]){t.Fatalf("checkpoint missing: %q %v",b,e)}
	if err=c.DownloadState(context.Background(),"mirror-state-test.mstate",dest);err!=nil{t.Fatal(err)}
	got,err:=os.ReadFile(dest)
	if err!=nil||!bytes.Equal(got,payload)||!rangeOK.Load(){t.Fatalf("bad resumed result %q range=%v err=%v",got,rangeOK.Load(),err)}
	if _,err=os.Stat(dest+".part.json");!os.IsNotExist(err){t.Fatal("checkpoint retained after success")}
}
