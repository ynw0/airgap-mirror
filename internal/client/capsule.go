package client

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/domain"
	storesqlite "github.com/ynw0/airgap-mirror/internal/storage/sqlite"
)

const maxCapsuleStateJSON = 8 << 20

type ImportedCapsule struct {
	Capsule      domain.StateCapsule `json:"capsule"`
	Root         string              `json:"root"`
	CatalogPaths map[string]string   `json:"catalogPaths"`
}

func validSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == sha256.Size
}

func capsuleFileMap(zr *zip.ReadCloser) (map[string]*zip.File, error) {
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		name := filepath.ToSlash(f.Name)
		if f.FileInfo().IsDir() {
			return nil, fmt.Errorf("State Capsule may not contain directory entries: %w", domain.ErrInvalid)
		}
		if f.Mode()&os.ModeSymlink != 0 || name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || filepath.ToSlash(filepath.Clean(name)) != name || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("invalid State Capsule entry %q: %w", f.Name, domain.ErrInvalid)
		}
		if _, exists := files[name]; exists {
			return nil, fmt.Errorf("duplicate State Capsule entry %q: %w", name, domain.ErrConflict)
		}
		files[name] = f
	}
	return files, nil
}

func readCapsuleState(f *zip.File) (domain.StateCapsule, error) {
	var cap domain.StateCapsule
	if f.UncompressedSize64 > maxCapsuleStateJSON {
		return cap, fmt.Errorf("State Capsule state.json exceeds %d bytes: %w", maxCapsuleStateJSON, domain.ErrInvalid)
	}
	r, err := f.Open()
	if err != nil {
		return cap, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, maxCapsuleStateJSON+1))
	if err != nil {
		return cap, err
	}
	if len(data) > maxCapsuleStateJSON {
		return cap, fmt.Errorf("State Capsule state.json too large: %w", domain.ErrInvalid)
	}
	if err = json.Unmarshal(data, &cap); err != nil {
		return cap, fmt.Errorf("decode State Capsule state.json: %w", err)
	}
	return cap, nil
}

func validateCapsuleManifest(cap domain.StateCapsule, files map[string]*zip.File) (map[string]domain.CapsuleCatalog, error) {
	if cap.SchemaVersion != domain.CapsuleSchemaVersion || cap.ID == "" {
		return nil, fmt.Errorf("unsupported State Capsule schema/id: %w", domain.ErrInvalid)
	}
	if len(cap.Sources) == 0 || len(cap.Catalogs) != len(cap.Sources) {
		return nil, fmt.Errorf("State Capsule source/catalog cardinality mismatch: %w", domain.ErrConflict)
	}
	sources := make(map[string]struct{}, len(cap.Sources))
	for _, item := range cap.Sources {
		if item.Source.ID == "" || item.State.SourceID != item.Source.ID {
			return nil, fmt.Errorf("State Capsule source state mismatch: %w", domain.ErrConflict)
		}
		if _, exists := sources[item.Source.ID]; exists {
			return nil, fmt.Errorf("duplicate State Capsule source %s: %w", item.Source.ID, domain.ErrConflict)
		}
		sources[item.Source.ID] = struct{}{}
	}
	catalogs := make(map[string]domain.CapsuleCatalog, len(cap.Catalogs))
	expectedFiles := map[string]struct{}{"state.json": {}}
	for _, cat := range cap.Catalogs {
		if _, ok := sources[cat.SourceID]; !ok {
			return nil, fmt.Errorf("catalog references unknown source %s: %w", cat.SourceID, domain.ErrConflict)
		}
		if _, exists := catalogs[cat.SourceID]; exists {
			return nil, fmt.Errorf("duplicate catalog for source %s: %w", cat.SourceID, domain.ErrConflict)
		}
		expected := filepath.ToSlash(filepath.Join("catalogs", cat.SourceID+".sqlite"))
		if cat.File != expected || cat.Size < 0 || !validSHA256Hex(strings.ToLower(cat.SHA256)) {
			return nil, fmt.Errorf("invalid catalog descriptor for source %s: %w", cat.SourceID, domain.ErrInvalid)
		}
		zf := files[cat.File]
		if zf == nil || zf.UncompressedSize64 != uint64(cat.Size) {
			return nil, fmt.Errorf("catalog %s size/entry mismatch: %w", cat.File, domain.ErrConflict)
		}
		catalogs[cat.SourceID] = cat
		expectedFiles[cat.File] = struct{}{}
	}
	if len(files) != len(expectedFiles) {
		return nil, fmt.Errorf("State Capsule contains undeclared entries: %w", domain.ErrInvalid)
	}
	for name := range files {
		if _, ok := expectedFiles[name]; !ok {
			return nil, fmt.Errorf("State Capsule undeclared entry %s: %w", name, domain.ErrInvalid)
		}
	}
	return catalogs, nil
}

func extractCatalog(ctx context.Context, zf *zip.File, descriptor domain.CapsuleCatalog, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(destination)
		}
	}()
	in, err := zf.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, descriptor.Size+1))
	if err != nil {
		return err
	}
	if n != descriptor.Size {
		return fmt.Errorf("catalog %s extracted size %d != %d: %w", descriptor.File, n, descriptor.Size, domain.ErrConflict)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, descriptor.SHA256) {
		return fmt.Errorf("catalog %s sha256 mismatch: %w", descriptor.File, domain.ErrConflict)
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func ImportStateCapsule(ctx context.Context, capsulePath, workspaceRoot string) (ImportedCapsule, error) {
	var out ImportedCapsule
	if capsulePath == "" || workspaceRoot == "" {
		return out, fmt.Errorf("capsule path and workspace root are required: %w", domain.ErrInvalid)
	}
	zr, err := zip.OpenReader(capsulePath)
	if err != nil {
		return out, err
	}
	defer zr.Close()
	files, err := capsuleFileMap(zr)
	if err != nil {
		return out, err
	}
	stateFile := files["state.json"]
	if stateFile == nil {
		return out, fmt.Errorf("State Capsule missing state.json: %w", domain.ErrInvalid)
	}
	cap, err := readCapsuleState(stateFile)
	if err != nil {
		return out, err
	}
	catalogs, err := validateCapsuleManifest(cap, files)
	if err != nil {
		return out, err
	}
	base := filepath.Join(workspaceRoot, "capsules")
	if err = os.MkdirAll(base, 0755); err != nil {
		return out, err
	}
	final := filepath.Join(base, cap.ID)
	if _, err = os.Stat(final); err == nil {
		return out, fmt.Errorf("State Capsule %s is already imported: %w", cap.ID, domain.ErrConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	tmp, err := os.MkdirTemp(base, ".import-")
	if err != nil {
		return out, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(tmp)
		}
	}()
	paths := make(map[string]string, len(catalogs))
	for sourceID, descriptor := range catalogs {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		dest := filepath.Join(tmp, filepath.FromSlash(descriptor.File))
		if err = extractCatalog(ctx, files[descriptor.File], descriptor, dest); err != nil {
			return out, err
		}
		db, snapshot, err := storesqlite.OpenCatalogSnapshot(dest)
		if err != nil {
			return out, err
		}
		err = snapshot.ValidateSource(ctx, sourceID)
		_ = db.Close()
		if err != nil {
			return out, fmt.Errorf("validate catalog %s: %w", sourceID, err)
		}
		paths[sourceID] = filepath.Join(final, filepath.FromSlash(descriptor.File))
	}
	stateJSON, err := json.MarshalIndent(cap, "", "  ")
	if err != nil {
		return out, err
	}
	if err = os.WriteFile(filepath.Join(tmp, "state.json"), stateJSON, 0644); err != nil {
		return out, err
	}
	if err = os.Rename(tmp, final); err != nil {
		return out, err
	}
	committed = true
	out = ImportedCapsule{Capsule: cap, Root: final, CatalogPaths: paths}
	return out, nil
}

func (c ImportedCapsule) Source(sourceID string) (domain.Source, domain.SourceState, string, error) {
	for _, item := range c.Capsule.Sources {
		if item.Source.ID != sourceID {
			continue
		}
		path := c.CatalogPaths[sourceID]
		if path == "" {
			return domain.Source{}, domain.SourceState{}, "", domain.ErrNotFound
		}
		s := domain.Source{ID: item.Source.ID, Name: item.Source.Name, Type: item.Source.Type, Provider: item.Source.Provider, UpstreamURL: item.Source.UpstreamURL, PublicURL: item.Source.PublicURL, Enabled: item.Source.Enabled, ConfigJSON: item.Source.ConfigJSON}
		return s, item.State, path, nil
	}
	return domain.Source{}, domain.SourceState{}, "", domain.ErrNotFound
}
