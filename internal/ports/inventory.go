package ports

import (
	"context"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type InventoryProgress struct {
	ScannedObjects    int64  `json:"scannedObjects"`
	ScannedBytes      int64  `json:"scannedBytes"`
	CompletedProjects int64  `json:"completedProjects,omitempty"`
	TotalProjects     int64  `json:"totalProjects,omitempty"`
	LastProject       string `json:"lastProject,omitempty"`
}

type InventoryRequest struct {
	Source  domain.Source
	Incremental bool
	Current CatalogStore
	Report  func(InventoryProgress) error
}

type InventoryAdapter interface {
	Inventory(context.Context, InventoryRequest, CatalogBuildSink) (domain.CatalogStats, error)
}

// ProjectInventorySink keeps a SQLite transaction across multiple projects.
// CheckpointProject commits completed projects and their cursor together so
// process termination never leaves an acknowledged half-project.
type ProjectInventorySink interface {
	CatalogBuildSink
	Restore(context.Context, string, string, int64) (InventoryProgress, error)
	CheckpointProject(context.Context, string, int64, int64) (InventoryProgress, bool, error)
	Flush(context.Context) (InventoryProgress, error)
	VerifyPages(context.Context, func(string, int64, int64) error) error
	Discard() error
}

type ProjectInventoryFactory interface {
	OpenProjectInventory(context.Context, string, string) (ProjectInventorySink, error)
	DiscardProjectInventory(string) error
}

// ProjectInventoryCache is optional. Cache entries are usable only if the
// complete page digest matches and every referenced file is checked again.
type ProjectInventoryCache interface {
	OpenPreviousCache(context.Context, string) error
	CachedProjectDigest(context.Context, string) (string, error)
	WalkCachedProject(context.Context, string, string, func(domain.CatalogEntry) error) error
	RecordProjectDigest(context.Context, string, string) error
	VerifyEntries(context.Context, func(domain.CatalogEntry) error) error
}

type ProjectInventoryCacheFactory interface {
	PromoteProjectInventory(string, string) error
}
