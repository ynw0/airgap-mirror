package service

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
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
	var cap domain.StateCapsule
	if c.Store == nil || c.Exporter == nil || c.OutputDir == "" {
		return cap, "", fmt.Errorf("capsule service not configured")
	}
	id, err := domain.NewID()
	if err != nil {
		return cap, "", err
	}
	cap = domain.StateCapsule{SchemaVersion: domain.CapsuleSchemaVersion, ID: id, ExportedAt: c.now()}
	if err = os.MkdirAll(c.OutputDir, 0755); err != nil {
		return cap, "", err
	}
	work, err := os.MkdirTemp(c.OutputDir, ".capsule-")
	if err != nil {
		return cap, "", err
	}
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
	for _, s := range sources {
		state, e := c.Store.GetState(ctx, s.ID)
		if e != nil {
			return cap, "", e
		}
		cap.Sources = append(cap.Sources, domain.CapsuleSource{Source: s.Public(), State: state})
		rel := filepath.ToSlash(filepath.Join("catalogs", s.ID+".sqlite"))
		local := filepath.Join(work, filepath.FromSlash(rel))
		if e = c.Exporter.ExportSource(ctx, s.ID, local); e != nil {
			return cap, "", e
		}
		h, n, e := pack.FileSHA256(local)
		if e != nil {
			return cap, "", e
		}
		cap.Catalogs = append(cap.Catalogs, domain.CapsuleCatalog{SourceID: s.ID, File: rel, SHA256: h, Size: n})
	}
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
		_, e = io.Copy(w, f)
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
	if err = os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return cap, "", err
	}
	return cap, final, nil
}
