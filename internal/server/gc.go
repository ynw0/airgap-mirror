package server

import (
	"net/http"
	"strconv"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type startGCRequest struct {
	Execute bool `json:"execute"`
}

func (h *MaintenanceHTTP) startGC(w http.ResponseWriter, r *http.Request) {
	var req startGCRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	job, err := h.Runner.StartGC(r.Context(), r.PathValue("id"), req.Execute)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (h *MaintenanceHTTP) candidates(w http.ResponseWriter, r *http.Request) {
	job, err := h.Jobs.GetMaintenanceJob(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	if job.Kind != domain.MaintenanceGC {
		writeError(w, http.StatusBadRequest, "maintenance job is not GC")
		return
	}
	if job.Status == domain.MaintenanceQueued || job.Status == domain.MaintenanceRunning {
		writeError(w, http.StatusConflict, "GC candidate plan is not finalized")
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil || n <= 0 || n > 1000 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 1000")
			return
		}
		limit = n
	}
	items, stats, err := h.Runner.ListGCCandidates(r.Context(), r.PathValue("id"), r.URL.Query().Get("after"), limit)
	if err != nil {
		mapError(w, err)
		return
	}
	next := ""
	if len(items) != 0 {
		next = items[len(items)-1].LogicalPath
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "stats": stats, "next": next})
}
