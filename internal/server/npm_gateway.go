package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"unicode"

	npmadapter "github.com/ynw0/airgap-mirror/internal/adapters/npm"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/service"
)

func (a *API) npmSource(r *http.Request) (domain.Source, error) {
	source, err := a.Store.GetSource(r.Context(), r.PathValue("sourceID"))
	if err != nil {
		return source, err
	}
	if source.Type != domain.SourceNPM || source.Provider != npmadapter.Provider || !source.Enabled {
		return domain.Source{}, domain.ErrNotFound
	}
	return source, nil
}

func (a *API) npmPing(w http.ResponseWriter, r *http.Request) {
	if _, err := a.npmSource(r); err != nil {
		mapError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func validNPMPackageName(name string) bool {
	if name == "" || strings.TrimSpace(name) != name || len(name) > 512 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.IsSpace(r) || r == '\\' {
			return false
		}
	}
	if strings.HasPrefix(name, "@") {
		parts := strings.Split(name, "/")
		return len(parts) == 2 && len(parts[0]) > 1 && parts[1] != ""
	}
	return !strings.Contains(name, "/")
}

func etagMatches(header, etag string) bool {
	for _, token := range strings.Split(header, ",") {
		token = strings.TrimSpace(token)
		if token == "*" || token == etag || strings.TrimPrefix(token, "W/") == etag {
			return true
		}
	}
	return false
}

func (a *API) npmPackument(w http.ResponseWriter, r *http.Request) {
	source, err := a.npmSource(r)
	if err != nil {
		mapError(w, err)
		return
	}
	name := r.PathValue("package")
	if !validNPMPackageName(name) {
		writeError(w, http.StatusBadRequest, "invalid npm package name")
		return
	}
	logical := npmadapter.PackumentLogicalPath(name)
	entry, err := a.Catalog.Get(r.Context(), source.ID, logical)
	if err != nil {
		mapError(w, err)
		return
	}
	if entry.PackageKey != "" && entry.PackageKey != name {
		mapError(w, fmt.Errorf("npm catalog package mismatch for %s: %w", name, domain.ErrConflict))
		return
	}
	if entry.SHA256 == "" || entry.Size < 0 {
		mapError(w, fmt.Errorf("npm catalog metadata is incomplete for %s: %w", name, domain.ErrConflict))
		return
	}

	f, info, err := service.OpenPublishedFile(source.RootPath, logical)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			mapError(w, domain.ErrNotFound)
		} else {
			mapError(w, err)
		}
		return
	}
	defer f.Close()
	if info.Size() != entry.Size {
		mapError(w, fmt.Errorf("npm published metadata size %d != catalog %d: %w", info.Size(), entry.Size, domain.ErrConflict))
		return
	}

	etag := `"` + strings.ToLower(entry.SHA256) + `"`
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
	if !entry.UpdatedAt.IsZero() {
		w.Header().Set("Last-Modified", entry.UpdatedAt.UTC().Format(http.TimeFormat))
	}
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, f)
}
