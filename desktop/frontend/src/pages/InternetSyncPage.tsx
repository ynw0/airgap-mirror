import { useEffect, useMemo, useState } from 'react'
import { app, onEvent } from '../backend'
import { errorText, formatBytes, formatCount, formatDate, shortID } from '../format'
import type { Batch, BatchDownloadProgress, Epoch, EpochView, StateCapsule, SyncPlan, UpstreamState } from '../types'

type Props = { capsules: StateCapsule[]; onCapsules: (items: StateCapsule[]) => void }

const TIB = 1024 ** 4
const GIB = 1024 ** 3

export function InternetSyncPage({ capsules, onCapsules }: Props) {
  const [capsuleID, setCapsuleID] = useState(capsules[0]?.id ?? '')
  const capsule = useMemo(() => capsules.find(x => x.id === capsuleID) ?? null, [capsules, capsuleID])
  const [sourceID, setSourceID] = useState('')
  const [probe, setProbe] = useState<UpstreamState | null>(null)
  const [plan, setPlan] = useState<SyncPlan | null>(null)
  const [epochView, setEpochView] = useState<EpochView | null>(null)
  const [history, setHistory] = useState<Epoch[]>([])
  const [destination, setDestination] = useState('')
  const [batchTiB, setBatchTiB] = useState(2)
  const [packGiB, setPackGiB] = useState(32)
  const [concurrency, setConcurrency] = useState(4)
  const [progress, setProgress] = useState<Record<string, BatchDownloadProgress>>({})
  const [running, setRunning] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')

  useEffect(() => {
    if (!capsuleID && capsules[0]) setCapsuleID(capsules[0].id)
  }, [capsules, capsuleID])
  useEffect(() => {
    const first = capsule?.sources[0]?.source.id ?? ''
    if (!capsule?.sources.some(x => x.source.id === sourceID)) setSourceID(first)
    setProbe(null); setPlan(null); setEpochView(null); setHistory([])
  }, [capsuleID])
  useEffect(() => onEvent<{batchId: string; state: string; error?: string}>('download-state', p => {
    if (!p?.batchId) return
    setRunning(prev => { const n = new Set(prev); p.state === 'running' ? n.add(p.batchId) : n.delete(p.batchId); return n })
    if (p.error) setMessage(p.error)
    void refreshBatchProgress(p.batchId)
  }), [])

  async function reloadCapsules() { const items = await app().ListCapsules(); onCapsules(items); if (!capsuleID && items[0]) setCapsuleID(items[0].id) }
  async function importCapsule() {
    setBusy(true); setMessage('')
    try {
      const path = await app().PickCapsuleFile(); if (!path) return
      const imported = await app().ImportCapsule(path)
      await reloadCapsules(); setCapsuleID(imported.capsule.id); setMessage(`已导入 ${imported.capsule.id}`)
    } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }
  async function probeSource() {
    if (!capsuleID || !sourceID) return
    setBusy(true); setMessage('')
    try { setProbe(await app().ProbeSource(capsuleID, sourceID)) } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }
  async function analyze() {
    if (!capsuleID || !sourceID) return
    setBusy(true); setMessage('')
    try {
      const result = await app().AnalyzeSource(capsuleID, sourceID, { maxBatchBytes: Math.round(batchTiB * TIB), targetPackBytes: Math.round(packGiB * GIB), pageSize: 1000 })
      setPlan(result)
      if (!result.noChanges) setEpochView({ epoch: result.plan.epoch, batches: result.batches })
      setHistory(await app().ListEpochs(capsuleID, sourceID))
      setMessage(result.noChanges ? '上游游标未变化，无需下载。' : `已规划 ${result.batches.length} 个 Batch。`)
    } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }
  async function resume(epochID: string) {
    setBusy(true); setMessage('')
    try { setEpochView(await app().ResumeEpoch(epochID)) } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }
  async function chooseDestination() { const value = await app().PickDownloadDirectory(); if (value) setDestination(value) }
  async function refreshBatchProgress(batchID: string) {
    try { const p = await app().BatchProgress(batchID); setProgress(prev => ({ ...prev, [batchID]: p })) } catch { /* batch may not exist after workspace rotation */ }
  }
  async function download(batch: Batch) {
    let target = destination
    if (!target) {
      target = await app().PickDownloadDirectory()
      if (!target) return
      setDestination(target)
    }
    setRunning(prev => new Set(prev).add(batch.id)); setMessage('')
    try { const bundle = await app().DownloadBatch(batch.id, target, concurrency); setMessage(`Batch ${batch.sequence} READY：${bundle.path}`) }
    catch (e) { setMessage(errorText(e)) }
    finally { setRunning(prev => { const n = new Set(prev); n.delete(batch.id); return n }); await refreshBatchProgress(batch.id) }
  }
  async function pause(batchID: string) { try { await app().PauseDownload(batchID) } catch (e) { setMessage(errorText(e)) } }

  const batches = epochView?.batches ?? plan?.batches ?? []
  return <section className="page-stack">
    <div className="page-header"><div><h2>互联网同步</h2><p>导入内网状态，冻结 Epoch，按移动介质容量规划并断点下载。</p></div><button className="primary" disabled={busy} onClick={importCapsule}>导入 State Capsule</button></div>

    <div className="card form-grid sync-selector">
      <label>State Capsule<select value={capsuleID} onChange={e => setCapsuleID(e.target.value)}><option value="">请选择</option>{capsules.map(c => <option key={c.id} value={c.id}>{shortID(c.id)} · {formatDate(c.exportedAt)}</option>)}</select></label>
      <label>Source<select value={sourceID} onChange={e => { setSourceID(e.target.value); setProbe(null); setPlan(null); setEpochView(null) }}><option value="">请选择</option>{capsule?.sources.map(s => <option key={s.source.id} value={s.source.id}>{s.source.name} · {s.source.type}/{s.source.provider}</option>)}</select></label>
      <label>Batch 上限 (TiB)<input type="number" min="0.1" step="0.1" value={batchTiB} onChange={e => setBatchTiB(Number(e.target.value))} /></label>
      <label>Pack 目标 (GiB)<input type="number" min="1" step="1" value={packGiB} onChange={e => setPackGiB(Number(e.target.value))} /></label>
      <div className="button-row align-end"><button disabled={!sourceID || busy} onClick={probeSource}>Probe</button><button className="primary" disabled={!sourceID || busy} onClick={analyze}>Analyze / Plan</button></div>
    </div>

    {(probe || plan) && <div className="metrics-grid">
      <div className="metric"><span>Base Cursor</span><strong>{plan?.state.liveCursor.kind ?? capsule?.sources.find(x => x.source.id === sourceID)?.state.liveCursor.kind ?? '—'}</strong><small>{plan?.state.liveCursor.value ?? '—'}</small></div>
      <div className="metric"><span>Target Cursor</span><strong>{plan?.plan.epoch.targetCursor.kind ?? probe?.cursor.kind ?? '—'}</strong><small>{plan?.plan.epoch.targetCursor.value ?? probe?.cursor.value ?? '—'}</small></div>
      <div className="metric"><span>计划数据量</span><strong>{formatBytes(plan?.plan.epoch.totalBytes ?? 0)}</strong><small>{formatCount(plan?.plan.artifactCount ?? 0)} objects</small></div>
      <div className="metric"><span>Publish Units</span><strong>{formatCount(plan?.plan.publishUnitCount ?? 0)}</strong><small>{formatCount(plan?.plan.metadataCount ?? 0)} metadata</small></div>
    </div>}

    <div className="card">
      <div className="card-title"><span>Batch 下载</span><div className="path-picker compact"><input value={destination} onChange={e => setDestination(e.target.value)} placeholder="移动硬盘 / 下载目录" /><button onClick={chooseDestination}>浏览</button><label className="inline-label">并发<input type="number" min="1" max="16" value={concurrency} onChange={e => setConcurrency(Number(e.target.value))} /></label></div></div>
      {batches.length ? <div className="batch-grid">{batches.map(batch => {
        const p = progress[batch.id]; const total = p?.totalBytes ?? batch.plannedBytes; const done = p?.downloadedBytes ?? 0; const pct = total ? Math.min(100, done / total * 100) : (batch.status === 'READY' ? 100 : 0); const active = running.has(batch.id)
        return <article className="batch-card" key={batch.id}><div className="batch-head"><div><strong>Batch {batch.sequence}</strong><small>{shortID(batch.id)}</small></div><span className={`status status-${batch.status.toLowerCase()}`}>{p?.batch.status ?? batch.status}</span></div><div className="batch-stats"><span>{formatBytes(batch.plannedBytes)}</span><span>{formatCount(batch.objectCount)} objects</span><span>{batch.packCount} packs</span></div><div className="progress"><div style={{width: `${pct}%`}} /></div><small>{formatBytes(done)} / {formatBytes(total)} · {pct.toFixed(1)}%{p ? ` · ${p.verifiedEntries}/${p.totalEntries} verified` : ''}</small><div className="button-row"><button onClick={() => refreshBatchProgress(batch.id)}>刷新</button>{active ? <button onClick={() => pause(batch.id)}>暂停</button> : <button className="primary" disabled={batch.status === 'READY' || batch.status === 'IMPORTED'} onClick={() => download(batch)}>下载 / 继续</button>}</div></article>
      })}</div> : <div className="empty panel-empty">Analyze 后显示 Batch。</div>}
    </div>

    <div className="card table-card"><div className="card-title"><span>本地 Epoch 历史</span><button disabled={!sourceID} onClick={async () => sourceID && setHistory(await app().ListEpochs(capsuleID, sourceID))}>刷新</button></div><div className="table-wrap"><table><thead><tr><th>Epoch</th><th>状态</th><th>Base → Target</th><th>数据量</th><th>Batch</th><th>创建时间</th><th></th></tr></thead><tbody>{history.length ? history.map(e => <tr key={e.id}><td><code>{shortID(e.id)}</code></td><td><span className={`status status-${e.status.toLowerCase()}`}>{e.status}</span></td><td><small className="mono clip">{e.baseCursor.value} → {e.targetCursor.value}</small></td><td>{formatBytes(e.totalBytes)}<small>{formatCount(e.totalObjects)} objects</small></td><td>{e.totalBatches}</td><td>{formatDate(e.createdAt)}</td><td><button onClick={() => resume(e.id)}>打开</button></td></tr>) : <tr><td colSpan={7} className="empty">暂无历史。</td></tr>}</tbody></table></div></div>
    {message && <div className="notice">{message}</div>}
  </section>
}
