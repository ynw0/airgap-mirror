package ports

import (
	"context"
	"io"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type AnalyzeRequest struct {
	CapsuleID string
	Source    domain.Source
	Base      domain.Cursor
	Workset   KeySet
	Catalog   CatalogStore
}
type MetadataRequest struct {
	Source domain.Source
	Epoch  domain.Epoch
	Unit   domain.PublishUnit
}
type PublishValidationRequest struct {
	Source  domain.Source
	Epoch   domain.Epoch
	Unit    domain.PublishUnit
	Catalog CatalogStore
}

type PlanSink interface {
	Begin(context.Context, domain.Epoch, string) error
	PutPublishUnit(context.Context, domain.PublishUnit) error
	PutArtifact(context.Context, domain.Artifact) error
	Commit(context.Context, domain.EpochPlan) error
	Abort(context.Context, string, error) error
}
type SourceAdapter interface {
	Type() domain.SourceType
	Provider() string
	ValidateConfig(context.Context, domain.Source) error
	Probe(context.Context, domain.Source) (domain.UpstreamState, error)
	Analyze(context.Context, AnalyzeRequest, PlanSink) (domain.EpochPlan, error)
	MaterializeMetadata(context.Context, MetadataRequest) ([]domain.GeneratedFile, error)
	ValidatePublish(context.Context, PublishValidationRequest) error
}
type AdapterRegistry interface {
	Get(domain.SourceType, string) (SourceAdapter, error)
}

type KeySet interface {
	Reset(context.Context, string) error
	Add(context.Context, string, string) (bool, error)
	Walk(context.Context, string, func(string) error) error
}
type CatalogStore interface {
	Get(context.Context, string, string) (domain.CatalogEntry, error)
	ListByPackage(context.Context, string, string) ([]domain.CatalogEntry, error)
	Upsert(context.Context, domain.CatalogEntry) error
	Delete(context.Context, string, string) error
	Stats(context.Context, string) (domain.CatalogStats, error)
}
type CatalogExporter interface {
	ExportSource(context.Context, string, string) error
}

// ServerStore 是 Agent 的单一持久化边界。显式方法名避免 Go 中同名不同返回值接口冲突。
type ServerStore interface {
	CreateSource(context.Context, domain.Source) error
	UpdateSource(context.Context, domain.Source) error
	GetSource(context.Context, string) (domain.Source, error)
	ListSources(context.Context) ([]domain.Source, error)
	GetState(context.Context, string) (domain.SourceState, error)
	PutState(context.Context, domain.SourceState) error
	CreateEpoch(context.Context, domain.Epoch) error
	GetEpoch(context.Context, string) (domain.Epoch, error)
	ListEpochs(context.Context, string) ([]domain.Epoch, error)
	TransitionEpoch(context.Context, string, domain.EpochStatus, string) error
	CreateBatch(context.Context, domain.Batch) error
	GetBatch(context.Context, string) (domain.Batch, error)
	ListBatches(context.Context, string) ([]domain.Batch, error)
	TransitionBatch(context.Context, string, domain.BatchStatus, string) error
	UpsertPack(context.Context, domain.Pack) error
	GetPack(context.Context, string) (domain.Pack, error)
	ListPacks(context.Context, string) ([]domain.Pack, error)
	DeclarePublishUnit(context.Context, domain.PublishUnit) error
	GetPublishUnit(context.Context, string) (domain.PublishUnit, error)
	ListPublishUnits(context.Context, string) ([]domain.PublishUnit, error)
	MarkPublished(context.Context, string, time.Time) error
	RegisterImportBundle(context.Context, domain.BatchDescriptor, domain.ImportSession, []domain.ImportPack) (domain.ImportSession, error)
	GetImportSession(context.Context, string) (domain.ImportSession, error)
	GetImportSessionByRequest(context.Context, string) (domain.ImportSession, error)
	UpdateImportSession(context.Context, domain.ImportSession) error
	GetImportPack(context.Context, string, string) (domain.ImportPack, error)
	ListImportPacks(context.Context, string) ([]domain.ImportPack, error)
	UpdateImportPack(context.Context, domain.ImportPack) error
	RegisterManifest(context.Context, string) error
	CommitImportedEntry(context.Context, domain.ManifestEntry, domain.CatalogEntry, string) (bool, error)
	ListPublishMetadata(context.Context, string) ([]domain.PublishMetadataEntry, error)
	MarkImportPackCommitted(context.Context, string, string) error
	CountImportedBatches(context.Context, string) (int, error)
	CountPublishedUnits(context.Context, string) (int64, error)
	CompleteEpoch(context.Context, string, domain.CatalogStats, time.Time) error
	WriteAudit(context.Context, domain.AuditEvent) error
}

type PlannedArtifact struct {
	Ordinal  int64
	Artifact domain.Artifact
	BatchID  string
	PackID   string
}
type PlanStore interface {
	PlanSink
	ListUnassignedArtifacts(context.Context, string, int64, int) ([]PlannedArtifact, error)
	AssignArtifact(context.Context, string, string, string) error
	SetArtifactPackLocation(context.Context, string, domain.PackLocation) error
	FinalizeArtifact(context.Context, string, int64, string) error
}
type TransferPlanStore interface {
	CreateBatch(context.Context, domain.Batch) error
	UpdateBatch(context.Context, domain.Batch) error
	CreatePack(context.Context, domain.Pack) error
	UpdatePack(context.Context, domain.Pack) error
	AddDownloadEntry(context.Context, domain.Artifact, string, string) error
}
type TransferExecutionStore interface {
	GetEpoch(context.Context, string) (domain.Epoch, error)
	GetBatch(context.Context, string) (domain.Batch, error)
	ListPacks(context.Context, string) ([]domain.Pack, error)
	WalkPackArtifacts(context.Context, string, func(domain.Artifact) error) error
	UpdatePack(context.Context, domain.Pack) error
	UpdateBatch(context.Context, domain.Batch) error
}
type BundlePlanStore interface {
	ListPlannedPublishUnits(context.Context, string) ([]domain.PublishUnit, error)
	WalkBatchManifestEntries(context.Context, string, func(domain.ManifestEntry) error) error
}

type FetchRequest struct {
	URL          string
	Offset       int64
	ETag         string
	LastModified string
}
type FetchResponse struct {
	Body          io.ReadCloser
	StatusCode    int
	ContentLength int64
	ETag          string
	LastModified  string
	AcceptRanges  bool
}
type ArtifactFetcher interface {
	Open(context.Context, FetchRequest) (FetchResponse, error)
}
type DownloadProgressStore interface {
	EntryState(context.Context, string) (domain.DownloadEntryProgress, error)
	MarkEntryState(context.Context, domain.DownloadEntryProgress) error
}
type PackWriter interface {
	Append(context.Context, domain.PackEntry, io.Reader) (domain.PackLocation, error)
	Close() error
}
type PackReader interface {
	Header() domain.PackHeader
	Next(context.Context) (domain.PackRecord, io.Reader, error)
	Close() error
}
type ArtifactInstaller interface {
	Install(context.Context, domain.Source, domain.PackRecord, io.Reader) (domain.InstallResult, error)
}
type Publisher interface {
	Publish(context.Context, domain.Source, domain.Epoch, []domain.PublishUnit) error
}
type PublishGate interface {
	Check(context.Context, string, string, domain.PublishUnit) error
}
type CapacityInspector interface {
	Inspect(context.Context, string) (domain.Capacity, error)
}
type AuditSink interface {
	Write(context.Context, domain.AuditEvent) error
}
type Clock interface{ Now() time.Time }
