package ports

import (
	"context"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type MaintenanceProgress struct {
	ScannedObjects   int64
	ScannedBytes     int64
	CandidateObjects int64
	CandidateBytes   int64
	AffectedObjects  int64
	AffectedBytes    int64
}

type MaintenanceStore interface {
	CreateMaintenanceJob(context.Context, domain.MaintenanceJob) error
	GetMaintenanceJob(context.Context, string) (domain.MaintenanceJob, error)
	ListMaintenanceJobs(context.Context, string, int) ([]domain.MaintenanceJob, error)
	TransitionMaintenanceJob(context.Context, string, domain.MaintenanceStatus, string, time.Time) error
	UpdateMaintenanceProgress(context.Context, string, MaintenanceProgress) error
	SetMaintenanceCatalogPath(context.Context, string, string) error
	ReplaceCatalogFromFile(context.Context, string, string, domain.CatalogStats, time.Time) error
}

type CatalogBuildSink interface {
	Put(context.Context, domain.CatalogEntry) error
	Stats(context.Context) (domain.CatalogStats, error)
	Path() string
	Close() error
	Abort() error
}

type CatalogBuildFactory interface {
	Create(context.Context, string, string) (CatalogBuildSink, error)
}
