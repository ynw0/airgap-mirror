from pathlib import Path


def replace_once(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text(encoding='utf-8')
    if old not in text:
        raise SystemExit(f'patch marker not found in {path}: {old!r}')
    p.write_text(text.replace(old, new, 1), encoding='utf-8')


types = Path('desktop/frontend/src/types.ts')
text = types.read_text(encoding='utf-8')
append = '''\n\nexport type MaintenanceKind = 'INVENTORY' | 'GC'\nexport type MaintenanceStatus = 'QUEUED' | 'RUNNING' | 'COMPLETED' | 'FAILED' | 'CANCELLED'\nexport interface MaintenanceJob {\n  id: string; sourceId: string; kind: MaintenanceKind; status: MaintenanceStatus; execute: boolean;\n  scannedObjects: number; scannedBytes: number; candidateObjects: number; candidateBytes: number;\n  affectedObjects: number; affectedBytes: number; tempCatalogPath?: string; createdAt: string;\n  startedAt?: string; completedAt?: string; errorText?: string;\n}\nexport interface GCCandidate { logicalPath: string; size: number; modTimeUnixNano: number }\nexport interface GCStats { objects: number; bytes: number }\nexport interface GCCandidatesPage { items: GCCandidate[]; stats: GCStats; next: string }\n'''
if 'export type MaintenanceKind' not in text:
    text += append
types.write_text(text, encoding='utf-8')

replace_once(
    'desktop/frontend/src/backend.ts',
    '  TransferProgress,\n} from \'./types\'\n',
    '  TransferProgress, MaintenanceJob, GCCandidatesPage,\n} from \'./types\'\n',
)
replace_once(
    'desktop/frontend/src/backend.ts',
    '  PauseTransfer(batchID: string): Promise<void>\n',
    '  PauseTransfer(batchID: string): Promise<void>\n  StartInventory(sourceID: string): Promise<MaintenanceJob>\n  StartGC(sourceID: string, execute: boolean): Promise<MaintenanceJob>\n  ListMaintenance(sourceID: string, limit: number): Promise<MaintenanceJob[]>\n  GetMaintenance(jobID: string): Promise<MaintenanceJob>\n  CancelMaintenance(jobID: string): Promise<MaintenanceJob>\n  GCCandidates(jobID: string, after: string, limit: number): Promise<GCCandidatesPage>\n',
)

replace_once(
    'desktop/frontend/src/pages/SourceStatePage.tsx',
    "import { app } from '../backend'\n",
    "import { app } from '../backend'\nimport { MaintenancePanel } from '../components/MaintenancePanel'\n",
)
replace_once(
    'desktop/frontend/src/pages/SourceStatePage.tsx',
    '    <div className="card export-row">\n',
    '    <MaintenancePanel overview={overview} />\n\n    <div className="card export-row">\n',
)
