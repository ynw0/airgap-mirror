package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
	"github.com/ynw0/airgap-mirror/internal/service"
	storesqlite "github.com/ynw0/airgap-mirror/internal/storage/sqlite"
)

const (
	DefaultBatchBytes int64 = 2 * 1024 * 1024 * 1024 * 1024
	DefaultPackBytes  int64 = 32 * 1024 * 1024 * 1024
	maxBatchJSON            = 8 << 20
)

type Workspace struct {
	Root     string
	DB       *sql.DB
	Store    *storesqlite.ClientStore
	Registry ports.AdapterRegistry
	HTTP     *http.Client
}

type AnalyzeOptions struct {
	MaxBatchBytes  int64 `json:"maxBatchBytes"`
	TargetPackBytes int64 `json:"targetPackBytes"`
	PageSize       int   `json:"pageSize"`
}

type SyncPlan struct {
	CapsuleID string             `json:"capsuleId"`
	Source    domain.Source      `json:"source"`
	State     domain.SourceState `json:"state"`
	Plan      domain.EpochPlan   `json:"plan"`
	Batches   []domain.Batch     `json:"batches"`
	NoChanges bool               `json:"noChanges"`
}

type DownloadOptions struct {
	DestinationRoot string `json:"destinationRoot"`
	Concurrency     int    `json:"concurrency"`
}

type BatchBundle struct {
	Batch      domain.Batch           `json:"batch"`
	Descriptor domain.BatchDescriptor `json:"descriptor"`
	Path       string                 `json:"path"`
}

func OpenWorkspace(ctx context.Context, root string, httpClient *http.Client) (*Workspace, error) {
	if root == "" {
		return nil, fmt.Errorf("workspace root is required: %w", domain.ErrInvalid)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(abs, 0755); err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	db, err := sql.Open("sqlite", filepath.Join(abs, "client.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	if err = storesqlite.InitClient(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	registry, err := service.NewDefaultRegistry(httpClient)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Workspace{Root: abs, DB: db, Store: storesqlite.NewClientStore(db), Registry: registry, HTTP: httpClient}, nil
}

func (w *Workspace) Close() error {
	if w == nil || w.DB == nil {
		return nil
	}
	return w.DB.Close()
}

func (w *Workspace) ImportCapsule(ctx context.Context, capsulePath string) (ImportedCapsule, error) {
	imported, err := ImportStateCapsule(ctx, capsulePath, w.Root)
	if err != nil {
		return ImportedCapsule{}, err
	}
	if err = w.Store.StoreCapsule(ctx, imported.Capsule, imported.Root, imported.CatalogPaths); err != nil {
		_ = os.RemoveAll(imported.Root)
		return ImportedCapsule{}, err
	}
	return imported, nil
}

func (w *Workspace) LoadCapsule(ctx context.Context, capsuleID string) (ImportedCapsule, error) {
	cap, root, catalogs, err := w.Store.LoadCapsule(ctx, capsuleID)
	if err != nil {
		return ImportedCapsule{}, err
	}
	return ImportedCapsule{Capsule: cap, Root: root, CatalogPaths: catalogs}, nil
}

func (w *Workspace) ListCapsules(ctx context.Context) ([]domain.StateCapsule, error) {
	return w.Store.ListCapsules(ctx)
}

func (w *Workspace) ProbeSource(ctx context.Context, capsuleID, sourceID string) (domain.UpstreamState, error) {
	cap, err := w.LoadCapsule(ctx, capsuleID)
	if err != nil {
		return domain.UpstreamState{}, err
	}
	source, _, _, err := cap.Source(sourceID)
	if err != nil {
		return domain.UpstreamState{}, err
	}
	if !source.Enabled {
		return domain.UpstreamState{}, fmt.Errorf("source %s is disabled: %w", sourceID, domain.ErrInvalid)
	}
	adapter, err := w.Registry.Get(source.Type, source.Provider)
	if err != nil {
		return domain.UpstreamState{}, err
	}
	return adapter.Probe(ctx, source)
}

func normalizeAnalyzeOptions(opts AnalyzeOptions) AnalyzeOptions {
	if opts.MaxBatchBytes <= 0 {
		opts.MaxBatchBytes = DefaultBatchBytes
	}
	if opts.TargetPackBytes <= 0 {
		opts.TargetPackBytes = DefaultPackBytes
	}
	if opts.PageSize <= 0 {
		opts.PageSize = 1000
	}
	return opts
}

func (w *Workspace) AnalyzeSource(ctx context.Context, capsuleID, sourceID string, opts AnalyzeOptions) (SyncPlan, error) {
	var out SyncPlan
	cap, err := w.LoadCapsule(ctx, capsuleID)
	if err != nil {
		return out, err
	}
	source, state, catalogPath, err := cap.Source(sourceID)
	if err != nil {
		return out, err
	}
	out.CapsuleID, out.Source, out.State = capsuleID, source, state
	if !source.Enabled {
		return out, fmt.Errorf("source %s is disabled: %w", sourceID, domain.ErrInvalid)
	}
	if state.ActiveEpochID != "" {
		return out, fmt.Errorf("source %s already has active server epoch %s: %w", sourceID, state.ActiveEpochID, domain.ErrConflict)
	}
	if existing, findErr := w.Store.FindOpenEpoch(ctx, capsuleID, sourceID); findErr == nil {
		return out, fmt.Errorf("source %s already has local epoch %s in status %s: %w", sourceID, existing.ID, existing.Status, domain.ErrConflict)
	} else if !errors.Is(findErr, domain.ErrNotFound) {
		return out, findErr
	}
	catalogDB, catalog, err := storesqlite.OpenCatalogSnapshot(catalogPath)
	if err != nil {
		return out, err
	}
	defer catalogDB.Close()
	if err = catalog.ValidateSource(ctx, sourceID); err != nil {
		return out, err
	}
	adapter, err := w.Registry.Get(source.Type, source.Provider)
	if err != nil {
		return out, err
	}
	plan, err := adapter.Analyze(ctx, ports.AnalyzeRequest{
		CapsuleID: capsuleID,
		Source:    source,
		Base:      state.LiveCursor,
		Workset:   w.Store,
		Catalog:   catalog,
		Generated: service.GeneratedStore{Root: filepath.Join(w.Root, "generated")},
	}, w.Store)
	if err != nil {
		return out, err
	}
	if plan.Epoch.ID == "" || plan.Epoch.SourceID != sourceID || !plan.Epoch.BaseCursor.Equal(state.LiveCursor) {
		return out, fmt.Errorf("adapter returned inconsistent epoch identity/base cursor: %w", domain.ErrConflict)
	}
	if plan.ArtifactCount != plan.Epoch.TotalObjects || plan.PublishUnitCount != plan.Epoch.PublishUnitCount {
		return out, fmt.Errorf("adapter epoch summary differs from persisted plan summary: %w", domain.ErrConflict)
	}
	out.Plan = plan
	if plan.ArtifactCount == 0 && plan.Epoch.TargetCursor.Equal(plan.Epoch.BaseCursor) {
		out.NoChanges = true
		return out, nil
	}
	opts = normalizeAnalyzeOptions(opts)
	epoch, err := (service.BatchPlanner{
		Plans:           w.Store,
		Transfers:       w.Store,
		MaxBatchBytes:   opts.MaxBatchBytes,
		TargetPackBytes: opts.TargetPackBytes,
		PageSize:        opts.PageSize,
	}).Plan(ctx, plan.Epoch)
	if err != nil {
		return out, err
	}
	plan.Epoch = epoch
	out.Plan = plan
	out.Batches, err = w.Store.ListBatches(ctx, epoch.ID)
	return out, err
}

func (w *Workspace) ResumeEpoch(ctx context.Context, epochID string) (domain.Epoch, []domain.Batch, error) {
	epoch, err := w.Store.GetEpoch(ctx, epochID)
	if err != nil {
		return domain.Epoch{}, nil, err
	}
	batches, err := w.Store.ListBatches(ctx, epochID)
	return epoch, batches, err
}

func bundleDirectory(destinationRoot string, batch domain.Batch) (string, error) {
	if destinationRoot == "" {
		return "", fmt.Errorf("destination root is required: %w", domain.ErrInvalid)
	}
	root, err := filepath.Abs(destinationRoot)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(root, 0755); err != nil {
		return "", err
	}
	return filepath.Join(root, fmt.Sprintf("batch-%06d-%s", batch.Sequence, batch.ID)), nil
}

func ReadBatchDescriptor(bundleDir string) (domain.BatchDescriptor, error) {
	var descriptor domain.BatchDescriptor
	f, err := os.Open(filepath.Join(bundleDir, "batch.json"))
	if err != nil {
		return descriptor, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, maxBatchJSON+1))
	if err != nil {
		return descriptor, err
	}
	if len(body) > maxBatchJSON {
		return descriptor, fmt.Errorf("batch.json exceeds %d bytes: %w", maxBatchJSON, domain.ErrInvalid)
	}
	if err = json.Unmarshal(body, &descriptor); err != nil {
		return descriptor, fmt.Errorf("decode batch.json: %w", err)
	}
	if descriptor.SchemaVersion != domain.BundleSchemaVersion || descriptor.SourceID == "" || descriptor.EpochID == "" || descriptor.BatchID == "" || descriptor.ManifestSize < 0 || descriptor.ManifestSHA256 == "" {
		return descriptor, fmt.Errorf("invalid batch descriptor: %w", domain.ErrInvalid)
	}
	return descriptor, nil
}

func (w *Workspace) DownloadBatch(ctx context.Context, batchID string, opts DownloadOptions) (BatchBundle, error) {
	var out BatchBundle
	batch, err := w.Store.GetBatch(ctx, batchID)
	if err != nil {
		return out, err
	}
	epoch, err := w.Store.GetEpoch(ctx, batch.EpochID)
	if err != nil {
		return out, err
	}
	bundleDir, err := bundleDirectory(opts.DestinationRoot, batch)
	if err != nil {
		return out, err
	}
	if err = os.MkdirAll(bundleDir, 0755); err != nil {
		return out, err
	}
	if batch.Status == domain.BatchReady {
		descriptor, readErr := ReadBatchDescriptor(bundleDir)
		if readErr != nil {
			return out, readErr
		}
		if descriptor.BatchID != batch.ID || descriptor.EpochID != batch.EpochID {
			return out, fmt.Errorf("existing bundle identity differs from client database: %w", domain.ErrConflict)
		}
		return BatchBundle{Batch: batch, Descriptor: descriptor, Path: bundleDir}, nil
	}
	if epoch.Status == domain.EpochPlanned {
		if err = domain.ValidateEpochTransition(epoch.Status, domain.EpochDownloading); err != nil {
			return out, err
		}
		epoch.Status = domain.EpochDownloading
		if err = w.Store.UpdateEpoch(ctx, epoch); err != nil {
			return out, err
		}
	} else if epoch.Status != domain.EpochDownloading {
		return out, fmt.Errorf("epoch %s is %s and cannot download: %w", epoch.ID, epoch.Status, domain.ErrConflict)
	}

	switch batch.Status {
	case domain.BatchPlanned:
		if err = domain.ValidateBatchTransition(batch.Status, domain.BatchDownloading); err != nil {
			return out, err
		}
		batch.Status = domain.BatchDownloading
		if err = w.Store.UpdateBatch(ctx, batch); err != nil {
			return out, err
		}
		fallthrough
	case domain.BatchDownloading:
		downloader := service.Downloader{
			Plans:       w.Store,
			Exec:        w.Store,
			Progress:    w.Store,
			Fetcher:     service.HTTPFetcher{Client: w.HTTP},
			Concurrency: opts.Concurrency,
		}
		if err = downloader.DownloadBatch(ctx, batch.ID, bundleDir); err != nil {
			return out, err
		}
		batch, err = w.Store.GetBatch(ctx, batch.ID)
		if err != nil {
			return out, err
		}
		if err = domain.ValidateBatchTransition(batch.Status, domain.BatchVerifying); err != nil {
			return out, err
		}
		batch.Status = domain.BatchVerifying
		if err = w.Store.UpdateBatch(ctx, batch); err != nil {
			return out, err
		}
	case domain.BatchVerifying:
		// Download completed previously; retry only bundle finalization.
	default:
		return out, fmt.Errorf("batch %s is %s and cannot download/finalize: %w", batch.ID, batch.Status, domain.ErrConflict)
	}

	descriptor, err := (service.BundleFinalizer{
		Exec: w.Store,
		Plan: w.Store,
		OpenSQLite: func(path string) (*sql.DB, error) {
			return sql.Open("sqlite", path)
		},
	}).Finalize(ctx, batch.ID, bundleDir)
	if err != nil {
		return out, err
	}
	batch, err = w.Store.GetBatch(ctx, batch.ID)
	if err != nil {
		return out, err
	}
	batches, err := w.Store.ListBatches(ctx, batch.EpochID)
	if err != nil {
		return out, err
	}
	allReady := len(batches) == epoch.TotalBatches && len(batches) > 0
	for _, item := range batches {
		if item.Status != domain.BatchReady {
			allReady = false
			break
		}
	}
	if allReady && epoch.Status == domain.EpochDownloading {
		if err = domain.ValidateEpochTransition(epoch.Status, domain.EpochTransferring); err != nil {
			return out, err
		}
		epoch.Status = domain.EpochTransferring
		if err = w.Store.UpdateEpoch(ctx, epoch); err != nil {
			return out, err
		}
	}
	return BatchBundle{Batch: batch, Descriptor: descriptor, Path: bundleDir}, nil
}
