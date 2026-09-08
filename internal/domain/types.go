package domain

import (
	"encoding/json"
	"time"
)

type SourceType string

const (
	SourceAPT   SourceType = "apt"
	SourcePyPI  SourceType = "pypi"
	SourceNPM   SourceType = "npm"
	SourceMaven SourceType = "maven"
)

type Cursor struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

func (c Cursor) Equal(o Cursor) bool { return c.Kind == o.Kind && c.Value == o.Value }
func (c Cursor) Empty() bool         { return c.Kind == "" && c.Value == "" }

type Source struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Type        SourceType      `json:"type"`
	Provider    string          `json:"provider"`
	UpstreamURL string          `json:"upstreamUrl"`
	RootPath    string          `json:"rootPath,omitempty"`
	PublicURL   string          `json:"publicUrl"`
	Enabled     bool            `json:"enabled"`
	ConfigJSON  json.RawMessage `json:"config"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}
type PublicSource struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Type        SourceType      `json:"type"`
	Provider    string          `json:"provider"`
	UpstreamURL string          `json:"upstreamUrl"`
	PublicURL   string          `json:"publicUrl"`
	Enabled     bool            `json:"enabled"`
	ConfigJSON  json.RawMessage `json:"config"`
}

func (s Source) Public() PublicSource {
	return PublicSource{ID: s.ID, Name: s.Name, Type: s.Type, Provider: s.Provider, UpstreamURL: s.UpstreamURL, PublicURL: s.PublicURL, Enabled: s.Enabled, ConfigJSON: s.ConfigJSON}
}

type SourceState struct {
	SourceID       string    `json:"sourceId"`
	LiveCursor     Cursor    `json:"liveCursor"`
	CatalogVersion int64     `json:"catalogVersion"`
	LiveBytes      int64     `json:"liveBytes"`
	LiveObjects    int64     `json:"liveObjects"`
	ActiveEpochID  string    `json:"activeEpochId,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
}
type UpstreamState struct {
	Cursor     Cursor    `json:"cursor"`
	ObservedAt time.Time `json:"observedAt"`
	Summary    string    `json:"summary,omitempty"`
}
type EpochStatus string

const (
	EpochPlanned      EpochStatus = "PLANNED"
	EpochDownloading  EpochStatus = "DOWNLOADING"
	EpochTransferring EpochStatus = "TRANSFERRING"
	EpochPublishable  EpochStatus = "PUBLISHABLE"
	EpochPublished    EpochStatus = "PUBLISHED"
	EpochComplete     EpochStatus = "COMPLETE"
	EpochFailed       EpochStatus = "FAILED"
	EpochCancelled    EpochStatus = "CANCELLED"
)

type Epoch struct {
	ID               string      `json:"id"`
	SourceID         string      `json:"sourceId"`
	BaseCursor       Cursor      `json:"baseCursor"`
	TargetCursor     Cursor      `json:"targetCursor"`
	Status           EpochStatus `json:"status"`
	TotalBytes       int64       `json:"totalBytes"`
	TotalObjects     int64       `json:"totalObjects"`
	TotalBatches     int         `json:"totalBatches"`
	PublishUnitCount int64       `json:"publishUnitCount"`
	CreatedAt        time.Time   `json:"createdAt"`
	CompletedAt      *time.Time  `json:"completedAt,omitempty"`
	ErrorText        string      `json:"errorText,omitempty"`
}
type EpochPlan struct {
	Epoch            Epoch `json:"epoch"`
	ArtifactCount    int64 `json:"artifactCount"`
	PublishUnitCount int64 `json:"publishUnitCount"`
	MetadataCount    int64 `json:"metadataCount"`
}
type ArtifactOperation string

const (
	ArtifactAdd    ArtifactOperation = "ADD"
	ArtifactUpdate ArtifactOperation = "UPDATE"
	ArtifactDelete ArtifactOperation = "DELETE"
)

type Artifact struct {
	ID                string            `json:"id"`
	EpochID           string            `json:"epochId"`
	SourceID          string            `json:"sourceId"`
	LogicalPath       string            `json:"logicalPath"`
	Size              int64             `json:"size"`
	SHA256            string            `json:"sha256,omitempty"`
	UpstreamURL       string            `json:"upstreamUrl,omitempty"`
	UpstreamIntegrity string            `json:"upstreamIntegrity,omitempty"`
	LocalSourcePath   string            `json:"-"`
	Operation         ArtifactOperation `json:"operation"`
	PublishUnitID     string            `json:"publishUnitId"`
	PackageKey        string            `json:"packageKey,omitempty"`
	Version           string            `json:"version,omitempty"`
	Metadata          bool              `json:"metadata"`
	Attributes        json.RawMessage   `json:"attributes,omitempty"`
}
type PublishUnitStatus string

const (
	PublishWaiting   PublishUnitStatus = "WAITING"
	PublishReady     PublishUnitStatus = "READY"
	PublishPublished PublishUnitStatus = "PUBLISHED"
	PublishConflict  PublishUnitStatus = "CONFLICT"
)

type PublishUnit struct {
	ID           string            `json:"id"`
	EpochID      string            `json:"epochId"`
	SourceID     string            `json:"sourceId"`
	Key          string            `json:"key"`
	Status       PublishUnitStatus `json:"status"`
	Required     int64             `json:"required"`
	Imported     int64             `json:"imported"`
	MetadataPath string            `json:"metadataPath,omitempty"`
	PublishedAt  *time.Time        `json:"publishedAt,omitempty"`
}
type BatchStatus string

const (
	BatchPlanned      BatchStatus = "PLANNED"
	BatchDownloading  BatchStatus = "DOWNLOADING"
	BatchVerifying    BatchStatus = "VERIFYING"
	BatchReady        BatchStatus = "READY"
	BatchTransferring BatchStatus = "TRANSFERRING"
	BatchImporting    BatchStatus = "IMPORTING"
	BatchImported     BatchStatus = "IMPORTED"
	BatchFailed       BatchStatus = "FAILED"
)

type Batch struct {
	ID           string      `json:"id"`
	EpochID      string      `json:"epochId"`
	SourceID     string      `json:"sourceId"`
	Sequence     int         `json:"sequence"`
	Status       BatchStatus `json:"status"`
	PlannedBytes int64       `json:"plannedBytes"`
	ObjectCount  int64       `json:"objectCount"`
	PackCount    int         `json:"packCount"`
	CreatedAt    time.Time   `json:"createdAt"`
	ImportedAt   *time.Time  `json:"importedAt,omitempty"`
	ErrorText    string      `json:"errorText,omitempty"`
}
type PackStatus string

const (
	PackPlanned   PackStatus = "PLANNED"
	PackWriting   PackStatus = "WRITING"
	PackReady     PackStatus = "READY"
	PackUploading PackStatus = "UPLOADING"
	PackUploaded  PackStatus = "UPLOADED"
	PackImporting PackStatus = "IMPORTING"
	PackImported  PackStatus = "IMPORTED"
	PackFailed    PackStatus = "FAILED"
)

type Pack struct {
	ID           string     `json:"id"`
	EpochID      string     `json:"epochId"`
	BatchID      string     `json:"batchId"`
	Sequence     int        `json:"sequence"`
	Size         int64      `json:"size"`
	SHA256       string     `json:"sha256,omitempty"`
	EntryCount   int64      `json:"entryCount"`
	UploadedSize int64      `json:"uploadedSize"`
	FilePath     string     `json:"filePath,omitempty"`
	Status       PackStatus `json:"status"`
}
type CatalogEntry struct {
	SourceID    string          `json:"sourceId"`
	LogicalPath string          `json:"logicalPath"`
	Size        int64           `json:"size"`
	SHA256      string          `json:"sha256"`
	PackageKey  string          `json:"packageKey,omitempty"`
	Version     string          `json:"version,omitempty"`
	Attributes  json.RawMessage `json:"attributes,omitempty"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}
type CatalogStats struct {
	SourceID string `json:"sourceId"`
	Bytes    int64  `json:"bytes"`
	Objects  int64  `json:"objects"`
}
type GeneratedFile struct {
	LogicalPath string `json:"logicalPath"`
	Content     []byte `json:"-"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
}
type DownloadEntryStatus string

const (
	DownloadPending     DownloadEntryStatus = "PENDING"
	DownloadDownloading DownloadEntryStatus = "DOWNLOADING"
	DownloadVerified    DownloadEntryStatus = "VERIFIED"
	DownloadFailed      DownloadEntryStatus = "FAILED"
)

type DownloadEntryProgress struct {
	EntryID      string              `json:"entryId"`
	Status       DownloadEntryStatus `json:"status"`
	Downloaded   int64               `json:"downloaded"`
	ETag         string              `json:"etag,omitempty"`
	LastModified string              `json:"lastModified,omitempty"`
	ErrorText    string              `json:"errorText,omitempty"`
}
type PackEntry struct {
	Artifact Artifact `json:"artifact"`
}
type PackLocation struct {
	PackID       string `json:"packId"`
	Offset       int64  `json:"offset"`
	RecordLength int64  `json:"recordLength"`
}
type PackHeader struct {
	PackID              string `json:"packId"`
	EpochID             string `json:"epochId"`
	BatchID             string `json:"batchId"`
	RecordCount         uint64 `json:"recordCount"`
	LogicalPayloadBytes uint64 `json:"logicalPayloadBytes"`
	CreatedUnix         int64  `json:"createdUnix"`
}
type PackRecord struct {
	EntryID       string `json:"entryId"`
	LogicalPath   string `json:"logicalPath"`
	ContentLength int64  `json:"contentLength"`
	SHA256        string `json:"sha256"`
	Offset        int64  `json:"offset"`
	RecordLength  int64  `json:"recordLength"`
}
type InstallResult struct {
	LogicalPath string `json:"logicalPath"`
	Bytes       int64  `json:"bytes"`
	Skipped     bool   `json:"skipped"`
}
type Capacity struct {
	Path       string `json:"path"`
	TotalBytes uint64 `json:"totalBytes"`
	FreeBytes  uint64 `json:"freeBytes"`
}
type AuditEvent struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	SourceID  string          `json:"sourceId,omitempty"`
	EpochID   string          `json:"epochId,omitempty"`
	BatchID   string          `json:"batchId,omitempty"`
	Message   string          `json:"message"`
	Detail    json.RawMessage `json:"detail,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}
type ImportSession struct {
	ID               string    `json:"id"`
	RequestID        string    `json:"requestId"`
	SourceID         string    `json:"sourceId"`
	EpochID          string    `json:"epochId"`
	BatchID          string    `json:"batchId"`
	ManifestPath     string    `json:"manifestPath"`
	ManifestSize     int64     `json:"manifestSize"`
	ManifestSHA256   string    `json:"manifestSha256"`
	ManifestUploaded int64     `json:"manifestUploaded"`
	Status           string    `json:"status"`
	StagingPath      string    `json:"stagingPath"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
	ErrorText        string    `json:"errorText,omitempty"`
}
type ImportPack struct {
	SessionID      string     `json:"sessionId"`
	PackID         string     `json:"packId"`
	ExpectedSize   int64      `json:"expectedSize"`
	ExpectedSHA256 string     `json:"expectedSha256"`
	UploadedSize   int64      `json:"uploadedSize"`
	StagingPath    string     `json:"stagingPath"`
	Status         PackStatus `json:"status"`
}

type PublishMetadataEntry struct {
	EntryID       string          `json:"entryId"`
	PublishUnitID string          `json:"publishUnitId"`
	SourceID      string          `json:"sourceId"`
	LogicalPath   string          `json:"logicalPath"`
	Size          int64           `json:"size"`
	SHA256        string          `json:"sha256"`
	StagedPath    string          `json:"stagedPath"`
	PackageKey    string          `json:"packageKey,omitempty"`
	Version       string          `json:"version,omitempty"`
	Attributes    json.RawMessage `json:"attributes,omitempty"`
}
