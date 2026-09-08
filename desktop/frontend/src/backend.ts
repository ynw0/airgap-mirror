import type {
  AgentOverview, AnalyzeOptions, BatchBundle, BatchDownloadProgress, Bootstrap, DesktopTransferResult,
  Epoch, EpochView, ImportedCapsule, StateCapsule, StateExportResult, SyncPlan, UpstreamState, ValidatedBundle,
  TransferProgress, MaintenanceJob, GCCandidatesPage,
} from './types'

type AppBridge = {
  Bootstrap(): Promise<Bootstrap>
  ConnectAgent(baseURL: string, token: string): Promise<AgentOverview>
  RefreshAgent(): Promise<AgentOverview>
  DisconnectAgent(): Promise<void>
  PickStateDestination(): Promise<string>
  ExportStateCapsule(sourceIDs: string[], destination: string): Promise<StateExportResult>
  PickCapsuleFile(): Promise<string>
  PickDownloadDirectory(): Promise<string>
  PickBatchDirectory(): Promise<string>
  ImportCapsule(path: string): Promise<ImportedCapsule>
  ListCapsules(): Promise<StateCapsule[]>
  ProbeSource(capsuleID: string, sourceID: string): Promise<UpstreamState>
  AnalyzeSource(capsuleID: string, sourceID: string, opts: AnalyzeOptions): Promise<SyncPlan>
  ListEpochs(capsuleID: string, sourceID: string): Promise<Epoch[]>
  ResumeEpoch(epochID: string): Promise<EpochView>
  DownloadBatch(batchID: string, destination: string, concurrency: number): Promise<BatchBundle>
  PauseDownload(batchID: string): Promise<void>
  BatchProgress(batchID: string): Promise<BatchDownloadProgress>
  ValidateBatchDirectory(path: string): Promise<ValidatedBundle>
  TransferBatchDirectory(path: string, opts: {chunkSize: number; requestId?: string}): Promise<DesktopTransferResult>
  PauseTransfer(batchID: string): Promise<void>
  StartInventory(sourceID: string): Promise<MaintenanceJob>
  StartGC(sourceID: string, execute: boolean): Promise<MaintenanceJob>
  ListMaintenance(sourceID: string, limit: number): Promise<MaintenanceJob[]>
  GetMaintenance(jobID: string): Promise<MaintenanceJob>
  CancelMaintenance(jobID: string): Promise<MaintenanceJob>
  GCCandidates(jobID: string, after: string, limit: number): Promise<GCCandidatesPage>
}

declare global {
  interface Window {
    go?: { main?: { App?: AppBridge } }
    runtime?: {
      EventsOn(name: string, callback: (...data: unknown[]) => void): () => void
    }
  }
}

export function app(): AppBridge {
  const bridge = window.go?.main?.App
  if (!bridge) throw new Error('Wails App bridge is unavailable')
  return bridge
}

export function onEvent<T>(name: string, callback: (payload: T) => void): () => void {
  const runtime = window.runtime
  if (!runtime) return () => undefined
  return runtime.EventsOn(name, (...data: unknown[]) => callback(data[0] as T))
}
