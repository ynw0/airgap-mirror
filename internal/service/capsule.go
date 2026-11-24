package service

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

type CapsuleService struct {
	Store     ports.ServerStore
	Exporter  ports.CatalogExporter
	OutputDir string
	Now       func() time.Time
}

func (c CapsuleService) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
func (c CapsuleService) Export(ctx context.Context, sourceIDs []string) (domain.StateCapsule, string, error) {
	return c.export(ctx, sourceIDs, "")
}

func (c CapsuleService) ExportRepair(ctx context.Context, sourceID, jobID string) (domain.StateCapsule, string, error) {
	if sourceID == "" || jobID == "" {
		return domain.StateCapsule{}, "", domain.ErrInvalid
	}
	return c.export(ctx, []string{sourceID}, jobID)
}

func (c CapsuleService) export(ctx context.Context, sourceIDs []string, repairJobID string) (domain.StateCapsule, string, error) {
	id, err := domain.NewID()
	if err != nil {
		return domain.StateCapsule{}, "", err
	}
	return c.exportJob(ctx, sourceIDs, repairJobID, id, nil)
}

type exportContextReader struct {
	ctx context.Context
	r io.Reader
}
func (r exportContextReader) Read(p []byte)(int,error){
	if err:=r.ctx.Err();err!=nil{return 0,err}
	return r.r.Read(p)
}

func (c CapsuleService) exportJob(ctx context.Context, sourceIDs []string, repairJobID,id string, progress func(string,int)error) (domain.StateCapsule, string, error) {
	var cap domain.StateCapsule
	if c.Store == nil || c.Exporter == nil || c.OutputDir == "" {
		return cap, "", fmt.Errorf("capsule service not configured")
	}
	if id=="" || strings.ContainsAny(id,"/\\") || id=="." || id==".." {
		return cap,"",domain.ErrInvalid
	}
	var err error
	report:=func(stage string,done int)error{
		if err:=ctx.Err();err!=nil{return err}
		if progress!=nil{return progress(stage,done)}
		return nil
	}
	cap = domain.StateCapsule{SchemaVersion: domain.CapsuleSchemaVersion, ID: id, ExportedAt: c.now()}
	if repairJobID != "" {
		cap.SchemaVersion = domain.RepairCapsuleSchemaVersion
	}
	if err = os.MkdirAll(c.OutputDir, 0755); err != nil {
		return cap, "", err
	}
	// Job-owned workspace is reused only for an interrupted export with this
	// server-generated ID. No repository directory is involved.
	work := filepath.Join(c.OutputDir, ".capsule-job-"+id)
	if err = os.RemoveAll(work); err != nil {return cap,"",err}
	if err = os.Mkdir(work,0700); err != nil {return cap,"",err}
	defer os.RemoveAll(work)
	var sources []domain.Source
	if len(sourceIDs) == 0 {
		sources, err = c.Store.ListSources(ctx)
		if err != nil {
			return cap, "", err
		}
	} else {
		for _, sid := range sourceIDs {
			s, e := c.Store.GetSource(ctx, sid)
			if e != nil {
				return cap, "", e
			}
			sources = append(sources, s)
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	catalogDir := filepath.Join(work, "catalogs")
	if err = os.MkdirAll(catalogDir, 0755); err != nil {
		return cap, "", err
	}
	for i, s := range sources {
		if s.ID=="" || s.ID=="." || s.ID==".." || strings.ContainsAny(s.ID,"/\\") {return cap,"",domain.ErrInvalid}
		if err=report("导出 "+s.Name+" 的文件账本",i);err!=nil{return cap,"",err}
		rel := filepath.ToSlash(filepath.Join("catalogs", s.ID+".sqlite"))
		local := filepath.Join(work, filepath.FromSlash(rel))
		var state domain.SourceState
		var e error
		if repairJobID != "" {
			repair, ok := c.Exporter.(interface {
				ExportRepairSnapshot(context.Context, string, string, string) (domain.Source, domain.SourceState, error)
			})
			if !ok {
				return cap, "", domain.ErrInvalid
			}
			s, state, e = repair.ExportRepairSnapshot(ctx, s.ID, local, repairJobID)
		} else if snapshotter, ok := c.Exporter.(interface {
			ExportSourceSnapshot(context.Context, string, string) (domain.Source, domain.SourceState, error)
		}); ok {
			s, state, e = snapshotter.ExportSourceSnapshot(ctx, s.ID, local)
		} else {
			state, e = c.Store.GetState(ctx, s.ID)
			if e == nil {
				e = c.Exporter.ExportSource(ctx, s.ID, local)
			}
		}
		if e != nil {
			return cap, "", e
		}
		cap.Sources = append(cap.Sources, domain.CapsuleSource{Source: s.Public(), State: state})
		if err=report("计算 "+s.Name+" 的状态文件校验值",i);err!=nil{return cap,"",err}
		f,e:=os.Open(local)
		if e!=nil{return cap,"",e}
		hash:=sha256.New()
		n,e:=io.Copy(hash,exportContextReader{ctx:ctx,r:f})
		closeErr:=f.Close()
		if e!=nil{return cap,"",e}
		if closeErr!=nil{return cap,"",closeErr}
		cap.Catalogs = append(cap.Catalogs, domain.CapsuleCatalog{SourceID: s.ID, File: rel, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: n})
	}
	if err=report("压缩状态文件",len(sources));err!=nil{return cap,"",err}
	stateJSON, err := json.MarshalIndent(cap, "", "  ")
	if err != nil {
		return cap, "", err
	}
	statePath := filepath.Join(work, "state.json")
	if err = os.WriteFile(statePath, stateJSON, 0644); err != nil {
		return cap, "", err
	}
	final := filepath.Join(c.OutputDir, "mirror-state-"+id+".mstate")
	tmp := final + ".tmp"
	_ = os.Remove(tmp)
	zf, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return cap, "", err
	}
	zw := zip.NewWriter(zf)
	add := func(rel, path string) error {
		w, e := zw.Create(rel)
		if e != nil {
			return e
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		defer f.Close()
		_, e = io.Copy(w, exportContextReader{ctx:ctx,r:f})
		return e
	}
	if err = add("state.json", statePath); err == nil {
		for _, x := range cap.Catalogs {
			if err = add(x.File, filepath.Join(work, filepath.FromSlash(x.File))); err != nil {
				break
			}
		}
	}
	if e := zw.Close(); err == nil {
		err = e
	}
	if e := zf.Sync(); err == nil {
		err = e
	}
	if e := zf.Close(); err == nil {
		err = e
	}
	if err != nil {
		_ = os.Remove(tmp)
		return cap, "", err
	}
	if err=ctx.Err();err!=nil{_ = os.Remove(tmp);return cap,"",err}
	if err = os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return cap, "", err
	}
	return cap, final, nil
}
