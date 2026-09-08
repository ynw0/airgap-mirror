package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
	"github.com/ynw0/airgap-mirror/internal/service"
)

type API struct {
	Store       ports.ServerStore
	Catalog     ports.CatalogStore
	Registry    ports.AdapterRegistry
	Registrar   service.BundleRegistrar
	Transfer    service.TransferService
	Importer    service.ImportService
	Capsules    service.CapsuleService
	Capacity    ports.CapacityInspector
	BearerToken string
	Now         func() time.Time
}

func (a *API) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", a.health)
	// npm metadata is a repository data-plane endpoint; package clients do not use the Agent bearer token.
	mux.HandleFunc("GET /npm/{sourceID}/-/ping", a.npmPing)
	mux.HandleFunc("GET /npm/{sourceID}/{package...}", a.npmPackument)
	secure := func(h http.HandlerFunc) http.Handler { return a.auth(h) }
	mux.Handle("GET /api/v1/capacity", secure(a.capacity))
	mux.Handle("GET /api/v1/sources", secure(a.sourcesList))
	mux.Handle("POST /api/v1/sources", secure(a.sourcesCreate))
	mux.Handle("GET /api/v1/sources/{id}", secure(a.sourceGet))
	mux.Handle("PUT /api/v1/sources/{id}", secure(a.sourceUpdate))
	mux.Handle("GET /api/v1/sources/{id}/state", secure(a.sourceState))
	mux.Handle("GET /api/v1/epochs", secure(a.epochsList))
	mux.Handle("GET /api/v1/epochs/{id}", secure(a.epochGet))
	mux.Handle("GET /api/v1/epochs/{id}/batches", secure(a.epochBatches))
	mux.Handle("GET /api/v1/batches/{id}", secure(a.batchGet))
	mux.Handle("POST /api/v1/state/export", secure(a.stateExport))
	mux.Handle("GET /api/v1/state/exports/{name}", secure(a.stateDownload))
	mux.Handle("POST /api/v1/imports", secure(a.importCreate))
	mux.Handle("GET /api/v1/imports/{id}", secure(a.importGet))
	mux.Handle("HEAD /api/v1/imports/{id}/manifest", secure(a.manifestHead))
	mux.Handle("PATCH /api/v1/imports/{id}/manifest", secure(a.manifestPatch))
	mux.Handle("POST /api/v1/imports/{id}/manifest/complete", secure(a.manifestComplete))
	mux.Handle("HEAD /api/v1/imports/{id}/packs/{packID}", secure(a.packHead))
	mux.Handle("PATCH /api/v1/imports/{id}/packs/{packID}", secure(a.packPatch))
	mux.Handle("POST /api/v1/imports/{id}/packs/{packID}/commit", secure(a.packCommit))
	mux.Handle("POST /api/v1/imports/{id}/complete", secure(a.batchComplete))
	return mux
}
func (a *API) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.BearerToken != "" {
			h := r.Header.Get("Authorization")
			if h != "Bearer "+a.BearerToken {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
func mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, domain.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, domain.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
func decodeJSON(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("decode json: %w", err)
	}
	return nil
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": a.now().UTC()})
}
func (a *API) capacity(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sourceId")
	if sid == "" {
		writeError(w, 400, "sourceId is required")
		return
	}
	s, err := a.Store.GetSource(r.Context(), sid)
	if err != nil {
		mapError(w, err)
		return
	}
	c, err := a.Capacity.Inspect(r.Context(), s.RootPath)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, 200, c)
}
func (a *API) sourcesList(w http.ResponseWriter, r *http.Request) {
	v, err := a.Store.ListSources(r.Context())
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (a *API) sourcesCreate(w http.ResponseWriter, r *http.Request) {
	var s domain.Source
	if err := decodeJSON(r, &s); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if s.ID == "" {
		id, err := domain.NewID()
		if err != nil {
			mapError(w, err)
			return
		}
		s.ID = id
	}
	if s.Name == "" || s.Type == "" || s.Provider == "" || s.RootPath == "" {
		writeError(w, 400, "name,type,provider,rootPath are required")
		return
	}
	if a.Registry != nil {
		ad, err := a.Registry.Get(s.Type, s.Provider)
		if err != nil {
			mapError(w, err)
			return
		}
		if err = ad.ValidateConfig(r.Context(), s); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	now := a.now()
	s.CreatedAt = now
	s.UpdatedAt = now
	if err := a.Store.CreateSource(r.Context(), s); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, 201, s)
}
func (a *API) sourceGet(w http.ResponseWriter, r *http.Request) {
	s, err := a.Store.GetSource(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, 200, s)
}
func (a *API) sourceUpdate(w http.ResponseWriter, r *http.Request) {
	old, err := a.Store.GetSource(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	var s domain.Source
	if err = decodeJSON(r, &s); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	s.ID = old.ID
	s.CreatedAt = old.CreatedAt
	s.UpdatedAt = a.now()
	if a.Registry != nil {
		ad, e := a.Registry.Get(s.Type, s.Provider)
		if e != nil {
			mapError(w, e)
			return
		}
		if e = ad.ValidateConfig(r.Context(), s); e != nil {
			writeError(w, 400, e.Error())
			return
		}
	}
	if err = a.Store.UpdateSource(r.Context(), s); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, 200, s)
}
func (a *API) sourceState(w http.ResponseWriter, r *http.Request) {
	v, err := a.Store.GetState(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (a *API) epochsList(w http.ResponseWriter, r *http.Request) {
	v, err := a.Store.ListEpochs(r.Context(), r.URL.Query().Get("sourceId"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (a *API) epochGet(w http.ResponseWriter, r *http.Request) {
	v, err := a.Store.GetEpoch(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (a *API) epochBatches(w http.ResponseWriter, r *http.Request) {
	v, err := a.Store.ListBatches(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (a *API) batchGet(w http.ResponseWriter, r *http.Request) {
	v, err := a.Store.GetBatch(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

type exportRequest struct {
	SourceIDs []string `json:"sourceIds"`
}

func (a *API) stateExport(w http.ResponseWriter, r *http.Request) {
	var req exportRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	cap, path, err := a.Capsules.Export(r.Context(), req.SourceIDs)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"capsule": cap, "name": filepath.Base(path), "download": "/api/v1/state/exports/" + filepath.Base(path)})
}
func (a *API) stateDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if filepath.Base(name) != name || !strings.HasPrefix(name, "mirror-state-") || !strings.HasSuffix(name, ".mstate") {
		writeError(w, 400, "invalid export name")
		return
	}
	path := filepath.Join(a.Capsules.OutputDir, name)
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		writeError(w, 404, "export not found")
		return
	}
	if err != nil {
		mapError(w, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		mapError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(w, r, name, st.ModTime(), f)
}

type createImportRequest struct {
	RequestID  string                 `json:"requestId"`
	Descriptor domain.BatchDescriptor `json:"descriptor"`
}

func (a *API) importCreate(w http.ResponseWriter, r *http.Request) {
	var req createImportRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	v, err := a.Registrar.Register(r.Context(), req.RequestID, req.Descriptor)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, 201, v)
}
func (a *API) importGet(w http.ResponseWriter, r *http.Request) {
	s, err := a.Store.GetImportSession(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	packs, err := a.Store.ListImportPacks(r.Context(), s.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"session": s, "packs": packs})
}
func (a *API) manifestHead(w http.ResponseWriter, r *http.Request) {
	s, err := a.Store.GetImportSession(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	off, err := a.Transfer.ManifestOffset(r.Context(), s.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(off, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(s.ManifestSize, 10))
	w.WriteHeader(204)
}
func parsePatch(r *http.Request) (int64, int64, error) {
	off, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil {
		return 0, 0, domain.ErrInvalid
	}
	if r.ContentLength < 0 {
		return 0, 0, fmt.Errorf("Content-Length is required: %w", domain.ErrInvalid)
	}
	if r.ContentLength > service.MaxUploadChunk {
		return 0, 0, fmt.Errorf("chunk exceeds %d bytes: %w", service.MaxUploadChunk, domain.ErrInvalid)
	}
	return off, r.ContentLength, nil
}
func (a *API) manifestPatch(w http.ResponseWriter, r *http.Request) {
	off, n, err := parsePatch(r)
	if err != nil {
		mapError(w, err)
		return
	}
	next, err := a.Transfer.AppendManifest(r.Context(), r.PathValue("id"), off, r.Body, n)
	if err != nil {
		w.Header().Set("Upload-Offset", strconv.FormatInt(next, 10))
		mapError(w, err)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(next, 10))
	w.WriteHeader(204)
}
func (a *API) manifestComplete(w http.ResponseWriter, r *http.Request) {
	if err := a.Transfer.CompleteManifest(r.Context(), r.PathValue("id")); err != nil {
		mapError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *API) packHead(w http.ResponseWriter, r *http.Request) {
	p, err := a.Store.GetImportPack(r.Context(), r.PathValue("id"), r.PathValue("packID"))
	if err != nil {
		mapError(w, err)
		return
	}
	off, err := a.Transfer.PackOffset(r.Context(), p.SessionID, p.PackID)
	if err != nil {
		mapError(w, err)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(off, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(p.ExpectedSize, 10))
	w.WriteHeader(204)
}
func (a *API) packPatch(w http.ResponseWriter, r *http.Request) {
	off, n, err := parsePatch(r)
	if err != nil {
		mapError(w, err)
		return
	}
	next, err := a.Transfer.AppendPack(r.Context(), r.PathValue("id"), r.PathValue("packID"), off, r.Body, n)
	if err != nil {
		w.Header().Set("Upload-Offset", strconv.FormatInt(next, 10))
		mapError(w, err)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(next, 10))
	w.WriteHeader(204)
}
func (a *API) packCommit(w http.ResponseWriter, r *http.Request) {
	if err := a.Importer.CommitPack(r.Context(), r.PathValue("id"), r.PathValue("packID")); err != nil {
		mapError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *API) batchComplete(w http.ResponseWriter, r *http.Request) {
	if err := a.Importer.CompleteBatch(r.Context(), r.PathValue("id")); err != nil {
		mapError(w, err)
		return
	}
	w.WriteHeader(204)
}

func Shutdown(ctx context.Context, s *http.Server) error { return s.Shutdown(ctx) }
