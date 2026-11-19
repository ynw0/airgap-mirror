package server

import (
	"net/http"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func (h *MaintenanceHTTP) signAPT(w http.ResponseWriter, r *http.Request) {
	if h.API.Signer == nil {
		mapError(w, domain.ErrInvalid)
		return
	}
	result, err := h.API.Signer.Sign(r.Context(), r.PathValue("id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *MaintenanceHTTP) publicSigningKey(w http.ResponseWriter, r *http.Request) {
	if h.API.Signer == nil {
		mapError(w, domain.ErrInvalid)
		return
	}
	key, err := h.API.Signer.ExportPublicKey(r.Context())
	if err != nil {
		mapError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/pgp-keys")
	w.Header().Set("Content-Disposition", `attachment; filename="airgap-mirror-apt-key.asc"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(key)
}
