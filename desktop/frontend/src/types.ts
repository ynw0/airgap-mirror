export type SourceType = 'apt' | 'pypi' | 'npm' | 'maven'
export type EpochStatus = 'PLANNED' | 'DOWNLOADING' | 'TRANSFERRING' | 'PUBLISHABLE' | 'PUBLISHED' | 'COMPLETE' | 'FAILED' | 'CANCELLED'
export type BatchStatus = 'PLANNED' | 'DOWNLOADING' | 'VERIFYING' | 'READY' | 'TRANSFERRING' | 'IMPORTING' | 'IMPORTED' | 'FAILED'

export interface Cursor { kind: string; value: string }
export interface Source {
  id: string; name: string; type: SourceType; provider: string; upstreamUrl: string;
  rootPath?: string; publicUrl: string; enabled: boolean; config?: unknown;
}
export interface SourceState {
  sourceId: string; liveCursor: Cursor; catalogVersion: number; liveBytes: number;
  liveObjects: number; activeEpochId?: string; updatedAt: string;
}
export interface Capacity { path: string; totalBytes: number; freeBytes: number }
export interface AgentSourceView { source: Source; state: SourceState; capacity: Capacity }
export interface AgentOverview { connected: boolean; baseUrl: string; sources: AgentSourceView[] }

export interface CapsuleSource { source: Source; state: SourceState }
export interface StateCapsule {
  schemaVersion: number; id: string; exportedAt: string; sources: CapsuleSource[];
  catalogs?: Array<{sourceId: string; file: string; size: number; sha256: string}>;
}
export interface Bootstrap { workspaceRoot: string; capsules: StateCapsule[] }
export interface ImportedCapsule { capsule: StateCapsule; root: string; catalogPaths: Record<string, string> }
export interface UpstreamState { cursor: Cursor; observedAt: string; summary?: string }

export interface Epoch {
  id: string; sourceId: string; baseCursor: Cursor; targetCursor: Cursor; status: EpochStatus;
  totalBytes: number; totalObjects: number; totalBatches: number; publishUnitCount: number;
  createdAt: string; completedAt?: string; errorText?: string;
}
export interface Batch {
  id: string; epochId: string; sourceId: string; sequence: number; status: BatchStatus;
  plannedBytes: number; objectCount: number; packCount: number; createdAt: string;
  importedAt?: string; errorText?: string;
}
export interface EpochPlan { epoch: Epoch; artifactCount: number; publishUnitCount: number; metadataCount: number }
export interface SyncPlan {
  capsuleId: string; source: Source; state: SourceState; plan: EpochPlan; batches: Batch[]; noChanges: boolean;
}
export interface AnalyzeOptions { maxBatchBytes: number; targetPackBytes: number; pageSize: number }
export interface EpochView { epoch: Epoch; batches: Batch[] }
export interface BatchDownloadProgress {
  batch: Batch; totalBytes: number; downloadedBytes: number;
  totalEntries: number; verifiedEntries: number; failedEntries: number;
}
export interface BatchBundle { batch: Batch; descriptor: BatchDescriptor; path: string }

export interface PackSpec { id: string; sequence: number; file: string; size: number; sha256: string; entryCount: number }
export interface BatchDescriptor {
  schemaVersion: number; sourceId: string; epochId: string; batchId: string; batchSequence: number;
  batchBytes: number; batchObjects: number; epochTotalBytes: number; epochTotalObjects: number;
  epochTotalBatches: number; epochPublishUnitCount: number; baseCursor: Cursor; targetCursor: Cursor;
  manifestSha256: string; manifestSize: number; packs: PackSpec[]; createdAt: string;
}
export interface ValidatedBundle { descriptor: BatchDescriptor; root: string; manifestPath: string; packPaths: Record<string, string> }
export interface TransferProgress {
  kind: string; sessionId: string; packId?: string; path: string; transferred: number; total: number;
}
export interface ImportSession { id: string; requestId: string; sourceId: string; epochId: string; batchId: string; status: string }
export interface ImportPack { sessionId: string; packId: string; expectedSize: number; expectedSha256: string; uploadedSize: number; status: string }
export interface ImportStatus { session: ImportSession; packs: ImportPack[] }
export interface BundleTransferResult { descriptor: BatchDescriptor; status: ImportStatus }
export interface WorkspaceTransferResult { remote: BundleTransferResult; batch: Batch; epoch: Epoch }
export interface DesktopTransferResult {
  workspaceTracked: boolean; local?: WorkspaceTransferResult; standalone?: BundleTransferResult;
}
export interface StateExportResult { path: string; export: { capsule: StateCapsule; name: string; download: string } }


export type MaintenanceKind = 'INVENTORY' | 'GC'
export type MaintenanceStatus = 'QUEUED' | 'RUNNING' | 'COMPLETED' | 'FAILED' | 'CANCELLED'
export interface MaintenanceJob {
  id: string; sourceId: string; kind: MaintenanceKind; status: MaintenanceStatus; execute: boolean;
  scannedObjects: number; scannedBytes: number; candidateObjects: number; candidateBytes: number;
  affectedObjects: number; affectedBytes: number; tempCatalogPath?: string; createdAt: string;
  startedAt?: string; completedAt?: string; errorText?: string;
}
export interface GCCandidate { logicalPath: string; size: number; modTimeUnixNano: number }
export interface GCStats { objects: number; bytes: number }
export interface GCCandidatesPage { items: GCCandidate[]; stats: GCStats; next: string }
