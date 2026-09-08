package domain

import "time"

const (
	CapsuleSchemaVersion  = 1
	BundleSchemaVersion   = 1
	ManifestSchemaVersion = 1
)

type CapsuleCatalog struct {
	SourceID string `json:"sourceId"`
	File     string `json:"file"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}
type CapsuleSource struct {
	Source PublicSource `json:"source"`
	State  SourceState  `json:"state"`
}
type StateCapsule struct {
	SchemaVersion int              `json:"schemaVersion"`
	ID            string           `json:"id"`
	ExportedAt    time.Time        `json:"exportedAt"`
	Sources       []CapsuleSource  `json:"sources"`
	Catalogs      []CapsuleCatalog `json:"catalogs"`
}
type PackSpec struct {
	ID         string `json:"id"`
	Sequence   int    `json:"sequence"`
	File       string `json:"file"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	EntryCount int64  `json:"entryCount"`
}
type BatchDescriptor struct {
	SchemaVersion         int        `json:"schemaVersion"`
	SourceID              string     `json:"sourceId"`
	EpochID               string     `json:"epochId"`
	BatchID               string     `json:"batchId"`
	BatchSequence         int        `json:"batchSequence"`
	EpochTotalBytes       int64      `json:"epochTotalBytes"`
	EpochTotalObjects     int64      `json:"epochTotalObjects"`
	EpochTotalBatches     int        `json:"epochTotalBatches"`
	EpochPublishUnitCount int64      `json:"epochPublishUnitCount"`
	BatchBytes            int64      `json:"batchBytes"`
	BatchObjects          int64      `json:"batchObjects"`
	BaseCursor            Cursor     `json:"baseCursor"`
	TargetCursor          Cursor     `json:"targetCursor"`
	ManifestSHA256        string     `json:"manifestSha256"`
	ManifestSize          int64      `json:"manifestSize"`
	Packs                 []PackSpec `json:"packs"`
	CreatedAt             time.Time  `json:"createdAt"`
}
type ManifestEntry struct {
	EntryID      string   `json:"entryId"`
	Artifact     Artifact `json:"artifact"`
	PackID       string   `json:"packId"`
	PackOffset   int64    `json:"packOffset"`
	RecordLength int64    `json:"recordLength"`
}
