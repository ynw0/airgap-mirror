package server

import (
	"context"
	"net/http"

	"github.com/ynw0/airgap-mirror/internal/domain"
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
	mux.Handle("POST /api/v1/sources/{id}/repair-check", secure(h.startRepair))
	mux.Handle("POST /api/v1/sources/{id}/integrity-check", secure(h.startIntegrity))
	mux.Handle("GET /api/v1/maintenance/{id}/repair-issues", secure(h.repairIssues))
	mux.Handle("POST /api/v1/maintenance/{id}/repair-approval", secure(h.repairApproval))
	mux.Handle("POST /api/v1/sources/{id}/sign", secure(h.signAPT))
	mux.Handle("GET /api/v1/signing-key", secure(h.publicSigningKey))
	mux.Handle("POST /api/v1/sources/{id}/gc", secure(h.startGC))
	mux.Handle("GET /api/v1/maintenance", secure(h.list))
	mux.Handle("GET /api/v1/maintenance/{id}", secure(h.get))
	mux.Handle("GET /api/v1/maintenance/{id}/candidates", secure(h.candidates))
	mux.Handle("POST /api/v1/maintenance/{id}/cancel", secure(h.cancel))
	mux.Handle("POST /api/v1/maintenance/{id}/pause", secure(h.pause))
	mux.Handle("POST /api/v1/maintenance/{id}/resume", secure(h.resume))
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

func (h *MaintenanceHTTP) pause(w http.ResponseWriter, r *http.Request) {
	job, err := h.Runner.PauseInventory(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (h *MaintenanceHTTP) resume(w http.ResponseWriter, r *http.Request) {
	job, err := h.Runner.ResumeInventory(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (h *MaintenanceHTTP) startRepair(w http.ResponseWriter, r *http.Request) {
	job, err := h.Runner.StartRepair(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (h *MaintenanceHTTP) repairIssues(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, 400, "limit must be between 1 and 1000")
			return
		}
		limit = n
	}
	issues, err := h.Runner.RepairIssues(r.Context(), r.PathValue("id"), r.URL.Query().Get("after"), limit)
	if err != nil {
		mapError(w, err)
		return
	}
	next := ""
	if len(issues) == limit {
		next = issues[len(issues)-1].LogicalPath
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": issues, "next": next})
}

func (h *MaintenanceHTTP) startIntegrity(w http.ResponseWriter, r *http.Request) {
	req := struct {
		MiBPerSecond int `json:"mibPerSecond"`
	}{MiBPerSecond: 64}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	job, err := h.Runner.StartIntegrity(r.Context(), r.PathValue("id"), req.MiBPerSecond)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (h *MaintenanceHTTP) repairApproval(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Approve *bool `json:"approve"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Approve == nil {
		writeError(w, 400, "必须明确指定 approve")
		return
	}
	store, ok := h.Jobs.(interface {
		ApproveRepairReplacement(context.Context, string, bool) error
	})
	if !ok {
		mapError(w, domain.ErrInvalid)
		return
	}
	if err := store.ApproveRepairReplacement(r.Context(), r.PathValue("id"), *req.Approve); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"approved": *req.Approve})
}
