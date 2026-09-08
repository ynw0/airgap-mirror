package server

import (
	"net/http"
	"strconv"

	"github.com/ynw0/airgap-mirror/internal/ports"
	"github.com/ynw0/airgap-mirror/internal/service"
)

type MaintenanceHTTP struct {
	API    *API
	Runner *service.MaintenanceRunner
	Jobs   ports.MaintenanceStore
}

func WithMaintenance(base http.Handler, api *API, runner *service.MaintenanceRunner, jobs ports.MaintenanceStore) http.Handler {
	if base == nil || api == nil || runner == nil || jobs == nil {
		panic("maintenance HTTP dependencies are required")
	}
	h := &MaintenanceHTTP{API: api, Runner: runner, Jobs: jobs}
	mux := http.NewServeMux()
	secure := func(fn http.HandlerFunc) http.Handler { return api.auth(fn) }
	mux.Handle("POST /api/v1/sources/{id}/inventory", secure(h.startInventory))
	mux.Handle("GET /api/v1/maintenance", secure(h.list))
	mux.Handle("GET /api/v1/maintenance/{id}", secure(h.get))
	mux.Handle("POST /api/v1/maintenance/{id}/cancel", secure(h.cancel))
	mux.Handle("/", base)
	return mux
}

func (h *MaintenanceHTTP) startInventory(w http.ResponseWriter, r *http.Request) {
	job, err := h.Runner.StartInventory(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (h *MaintenanceHTTP) list(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > 1000 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 1000")
			return
		}
		limit = n
	}
	jobs, err := h.Jobs.ListMaintenanceJobs(r.Context(), r.URL.Query().Get("sourceId"), limit)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (h *MaintenanceHTTP) get(w http.ResponseWriter, r *http.Request) {
	job, err := h.Jobs.GetMaintenanceJob(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *MaintenanceHTTP) cancel(w http.ResponseWriter, r *http.Request) {
	job, err := h.Runner.Cancel(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}
