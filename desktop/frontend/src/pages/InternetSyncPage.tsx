import { useEffect, useMemo, useRef, useState } from 'react'
import { app, onEvent } from '../backend'
import { errorText, formatBytes, formatCount, formatDate, shortID, statusLabel } from '../format'
import type { Batch, BatchDownloadProgress, Epoch, EpochView, StateCapsule, SyncPlan, UpstreamState } from '../types'

type Props = { capsules: StateCapsule[]; onCapsules: (items: StateCapsule[]) => void }

const GIB = 1024 ** 3
const PAGE_SIZE = 36
function duration(seconds: number) { if (!Number.isFinite(seconds) || seconds < 0) return '—'; if (seconds < 60) return `${Math.ceil(seconds)} 秒`; if (seconds < 3600) return `${Math.ceil(seconds / 60)} 分钟`; if (seconds < 86400) return `${(seconds / 3600).toFixed(1)} 小时`; return `${(seconds / 86400).toFixed(1)} 天` }

export function InternetSyncPage({ capsules, onCapsules }: Props) {
  const [capsuleID, setCapsuleID] = useState(capsules[0]?.id ?? '')
  const capsule = useMemo(() => capsules.find(x => x.id === capsuleID) ?? null, [capsules, capsuleID])
  const [sourceID, setSourceID] = useState('')
  const [probe, setProbe] = useState<UpstreamState | null>(null)
  const [plan, setPlan] = useState<SyncPlan | null>(null)
  const [epochView, setEpochView] = useState<EpochView | null>(null)
  const [history, setHistory] = useState<Epoch[]>([])
  const [destination, setDestination] = useState('')
  const [batchGiB, setBatchGiB] = useState(8)
  const [packGiB, setPackGiB] = useState(1)
  const [concurrency, setConcurrency] = useState(4)
  const [progress, setProgress] = useState<Record<string, BatchDownloadProgress>>({})
  const samples = useRef<Record<string, { bytes: number; at: number; rate: number }>>({})
  const [rates, setRates] = useState<Record<string, number>>({})
  const [batchFilter, setBatchFilter] = useState('all')
  const [batchPage, setBatchPage] = useState(1)
  const [running, setRunning] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const [historyState, setHistoryState] = useState<{key: string; ok: boolean}>({key:'', ok:false})
  const [historyRetry, setHistoryRetry] = useState(0)
  const downloadOperation = useRef(false)
  const [message, setMessage] = useState('')
  const historyKey = capsuleID + '\u0000' + sourceID
  const validSource = !!capsule?.sources.some(x => x.source.id === sourceID)
  const historyReady = validSource && historyState.key === historyKey && historyState.ok
  const locked = busy || running.size > 0

  useEffect(() => {
    if (!capsuleID && capsules[0]) setCapsuleID(capsules[0].id)
  }, [capsules, capsuleID])
  useEffect(() => {
    const first = capsule?.sources[0]?.source.id ?? ''
    if (!capsule?.sources.some(x => x.source.id === sourceID)) setSourceID(first)
    setProbe(null); setPlan(null); setEpochView(null); setHistory([])
  }, [capsuleID])
  useEffect(() => {
    if (!capsuleID || !validSource) return
    let cancelled = false
    setHistoryState({key: historyKey, ok: false})
    app().ListEpochs(capsuleID, sourceID).then(async items => {
      if (cancelled) return
      setHistory(items ?? [])
      const unfinished = (items ?? []).find(e => !['COMPLETE', 'FAILED', 'CANCELLED'].includes(e.status))
      if (unfinished) {
        const view = await app().ResumeEpoch(unfinished.id)
        if (!cancelled) {
          setEpochView(view)
          setMessage('找到了上次没有完成的下载任务。已经下载的批次会保留，您可以直接继续。')
        }
      }
      if (!cancelled) setHistoryState({key: historyKey, ok: true})
    }).catch(e => { if (!cancelled) { setHistoryState({key: historyKey, ok: false}); setMessage('读取已有下载任务失败：' + errorText(e)) } })
    return () => { cancelled = true }
  }, [capsuleID, sourceID, historyRetry, validSource])

  useEffect(() => onEvent<{batchId: string; state: string; error?: string}>('download-state', p => {
    if (!p?.batchId) return
    // The initiating promise owns the operation lock until all refreshes have finished.
    if (p.error) setMessage(`下载中断了，已保存的部分可以继续使用。${p.error}`)
    void refreshBatchProgress(p.batchId)
  }), [])

  useEffect(() => {
    if (!running.size) return
    let disposed = false, pending = false
    async function poll() {
      if (pending) return
      pending = true
      try {
        const items = await Promise.all([...running].map(async id => { try { return [id, await app().BatchProgress(id)] as const } catch { return null } }))
        if (!disposed) {
          const now = performance.now(), observed: Record<string, number> = {}
          for (const item of items) { if (!item) continue; const [id, p] = item; const prior = samples.current[id]; const elapsed = prior ? (now - prior.at) / 1000 : 0; const delta = prior ? p.downloadedBytes - prior.bytes : -1; const instant = elapsed > 0 && delta >= 0 ? delta / elapsed : 0; const rate = delta > 0 && prior ? (prior.rate > 0 ? prior.rate * .6 + instant * .4 : instant) : 0; samples.current[id] = { bytes: p.downloadedBytes, at: now, rate }; observed[id] = rate }
          setRates(prev => ({ ...prev, ...observed }))
          setProgress(prev => { const next = { ...prev }; for (const item of items) if (item) next[item[0]] = item[1]; return next })
        }
      } finally { pending = false }
    }
    void poll()
    const timer = window.setInterval(poll, 1500)
    return () => { disposed = true; window.clearInterval(timer) }
  }, [running])

  async function reloadCapsules() { const items = await app().ListCapsules(); onCapsules(items); if (!capsuleID && items[0]) setCapsuleID(items[0].id) }
  async function importCapsule() {
    setBusy(true); setMessage('')
    try {
      const path = await app().PickCapsuleFile(); if (!path) return
      const imported = await app().ImportCapsule(path)
      await reloadCapsules(); setCapsuleID(imported.capsule.id); setMessage('状态文件已打开。选择软件源后，就可以查看需要下载的更新。')
    } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }
  async function probeSource() {
    if (!validSource || locked) return
    setBusy(true); setMessage('')
    try { setProbe(await app().ProbeSource(capsuleID, sourceID)); setMessage('已经读到上游状态，可以继续检查更新。') } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }
  async function analyze() {
    if (!historyReady || locked) return
    if (!Number.isSafeInteger(batchGiB) || !Number.isSafeInteger(packGiB) || batchGiB < 1 || packGiB < 1 || packGiB > batchGiB || !Number.isSafeInteger(batchGiB * GIB)) { setMessage('批次和文件包容量必须为正整数，文件包容量不能大于批次容量。'); return }
    setBusy(true); setMessage('')
    try {
      const result = await app().AnalyzeSource(capsuleID, sourceID, { maxBatchBytes: Math.round(batchGiB * GIB), targetPackBytes: Math.round(packGiB * GIB), pageSize: 1000 })
      setPlan(result)
      setEpochView(result.noChanges ? null : { epoch: result.plan.epoch, batches: result.batches })
      setHistory(await app().ListEpochs(capsuleID, sourceID))
      setMessage(result.noChanges ? '这次没有新文件需要下载。' : `找到了 ${result.batches.length} 批需要下载的文件。选好保存位置后，逐批开始下载。`)
    } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }
  async function resume(epochID: string) {
    setBusy(true); setMessage('')
    try { const next = await app().ResumeEpoch(epochID); setPlan(null); setProbe(null); setEpochView(next); setMessage('之前的下载任务已打开。没下载完的批次可以继续。') } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }
  async function chooseDestination() {
    try { const value = await app().PickDownloadDirectory(); if (value) setDestination(value) }
    catch (e) { setMessage('无法选择保存目录：' + errorText(e)) }
  }
  async function refreshBatchProgress(batchID: string) {
    try { const p = await app().BatchProgress(batchID); setProgress(prev => ({ ...prev, [batchID]: p })) } catch { /* batch may not exist after workspace rotation */ }
  }
  async function download(batch: Batch) {
    if (downloadOperation.current || locked || !historyReady) return
    downloadOperation.current = true
    setBusy(true)
    try { await performDownload(batch, false) }
    catch (e) { setMessage(errorText(e)) }
    finally { downloadOperation.current = false; setBusy(false) }
  }
  async function redownload(batch: Batch) {
    if (downloadOperation.current || locked || !historyReady) return
    if (!window.confirm(`确定重新下载第 ${batch.sequence} 批吗？这会清理这批文件原来的下载内容，其它批次不受影响。`)) return
    downloadOperation.current = true
    setBusy(true)
    try { await performDownload(batch, true) }
    catch (e) { setMessage(errorText(e)) }
    finally { downloadOperation.current = false; setBusy(false) }
  }
  async function performDownload(batch: Batch, replace: boolean) {
    if (!Number.isInteger(concurrency) || concurrency < 1 || concurrency > 16) { setMessage('同时下载的文件数必须为 1 至 16 的整数。'); return }
    let target = destination
    if (!target) {
      target = await app().PickDownloadDirectory()
      if (!target) return
      setDestination(target)
    }
    delete samples.current[batch.id]; setRates(prev => ({ ...prev, [batch.id]: 0 })); setRunning(prev => new Set(prev).add(batch.id)); setMessage('')
    try {
      const bundle = await (replace ? app().RedownloadBatch(batch.id, target, concurrency) : app().DownloadBatch(batch.id, target, concurrency))
      setMessage(`第 ${batch.sequence} 批已经下载好了，保存在：${bundle.path}。把这个文件夹带回内网，在“离线导入”页面打开。`)
      setEpochView(await app().ResumeEpoch(batch.epochId))
    }
    catch (e) { setMessage(errorText(e)) }
    finally { await refreshBatchProgress(batch.id); setRunning(prev => { const n = new Set(prev); n.delete(batch.id); return n }) }
  }
  async function pause(batchID: string) { try { await app().PauseDownload(batchID) } catch (e) { setMessage(errorText(e)) } }

  const batches = epochView?.batches ?? plan?.batches ?? []
  const batchState = (batch: Batch) => progress[batch.id]?.batch.status ?? batch.status
  const isFinished = (batch: Batch) => ['READY', 'IMPORTED'].includes(batchState(batch))
  const finishedCount = batches.filter(isFinished).length
  const failedCount = batches.filter(b => batchState(b) === 'FAILED').length
  const filteredBatches = batches.filter(b => batchFilter === 'all' || (batchFilter === 'pending' && !isFinished(b)) || (batchFilter === 'finished' && isFinished(b)) || (batchFilter === 'failed' && batchState(b) === 'FAILED'))
  const maxPage = Math.max(1, Math.ceil(filteredBatches.length / PAGE_SIZE))
  const page = Math.min(batchPage, maxPage)
  useEffect(() => { setBatchPage(1); setBatchFilter('all') }, [epochView?.epoch.id, capsuleID, sourceID])
  return <section className="page-stack">
    <div className="page-header"><div><h2>在联网电脑上下载更新</h2><p>打开刚才带出来的状态文件，选好软件源，程序就会找出需要下载的内容。下载过程中可以暂停，下次再继续。</p></div><button className="primary" disabled={locked} onClick={importCapsule}>打开状态文件</button></div>

    {message && <div className="notice" role="status" aria-live="polite">{message}</div>}
    <div className="card form-grid sync-selector">
      <label>已打开的状态文件<select disabled={locked} value={capsuleID} onChange={e => setCapsuleID(e.target.value)}><option value="">先打开状态文件</option>{capsules.map(c => <option key={c.id} value={c.id}>{shortID(c.id)} · {formatDate(c.exportedAt)}</option>)}</select></label>
      <label>软件源<select disabled={locked} value={sourceID} onChange={e => { setSourceID(e.target.value); setProbe(null); setPlan(null); setEpochView(null); setHistory([]); setMessage('') }}><option value="">选择要更新的软件源</option>{capsule?.sources.map(s => <option key={s.source.id} value={s.source.id}>{s.source.name}（{s.source.type}）{s.source.enabled ? '' : ' · 已停用'}</option>)}</select></label>
      <details className="advanced-options span-2"><summary>高级选项（默认无需修改）</summary><div className="form-grid">
        <label>每批最多 (GiB)<input type="number" min="1" step="1" disabled={locked} value={batchGiB} onChange={e => setBatchGiB(Number(e.target.value))} /></label>
        <label>单包容量 (GiB)<input type="number" min="1" step="1" disabled={locked} value={packGiB} onChange={e => setPackGiB(Number(e.target.value))} /></label>
      </div><p className="small">默认每批约 8 GiB，每个文件包约 1 GiB，更方便使用普通移动硬盘。实际文件包可能因单个大文件而超过设置值。</p></details>
      <div className="button-row align-end"><button disabled={!validSource || locked} onClick={probeSource}>查看上游状态</button><button className="primary" disabled={!historyReady || locked || !!(epochView && !['COMPLETE', 'FAILED', 'CANCELLED'].includes(epochView.epoch.status))} onClick={analyze}>查看需要下载的更新</button></div>
    </div>

    {validSource && !historyReady && <div className="card guidance"><p>正在读取或尚未成功读取此软件源的已有下载任务。仍可查看上游状态；重新分析或下载前请先读取已有任务。</p><button disabled={locked} onClick={() => setHistoryRetry(n => n + 1)}>重新读取任务</button></div>}
    {(probe || plan) && <div className="metrics-grid">
      <div className="metric"><span>内网当前版本标记</span><strong>{plan?.state.liveCursor.kind ?? capsule?.sources.find(x => x.source.id === sourceID)?.state.liveCursor.kind ?? '—'}</strong><small>{plan?.state.liveCursor.value ?? capsule?.sources.find(x => x.source.id === sourceID)?.state.liveCursor.value ?? '—'}</small></div>
      <div className="metric"><span>这次更新的版本标记</span><strong>{plan?.plan.epoch.targetCursor.kind ?? probe?.cursor.kind ?? '—'}</strong><small>{plan?.plan.epoch.targetCursor.value ?? probe?.cursor.value ?? '—'}</small></div>
      <div className="metric"><span>预计需要下载</span><strong>{plan ? formatBytes(plan.plan.epoch.totalBytes) : '尚未分析'}</strong><small>{plan ? formatCount(plan.plan.artifactCount) : '—'} 个文件</small></div>
      <div className="metric"><span>需要更新的软件清单</span><strong>{plan ? formatCount(plan.plan.publishUnitCount) : '—'}</strong><small>其中 {plan ? formatCount(plan.plan.metadataCount) : '—'} 份索引文件</small></div>
    </div>}

    {epochView && !['COMPLETE', 'FAILED', 'CANCELLED'].includes(epochView.epoch.status) && <div className="card">
      <strong>{epochView.epoch.status === 'TRANSFERRING' ? '下载已准备好，可以带回内网导入' : '继续上次的下载任务'}</strong>
      <p>这次共 {epochView.batches.length} 批。如果有没下载完的批次，直接点击该批次的“下载 / 继续”。不需要重新计算更新。</p>
    </div>}
    <div className="card">
      <div className="card-title"><span>把文件保存到移动硬盘</span><div className="path-picker compact"><input disabled={locked} value={destination} onChange={e => setDestination(e.target.value)} placeholder="选择移动硬盘或其他保存位置" /><button disabled={locked} onClick={chooseDestination}>浏览</button><details className="advanced-options compact-options"><summary>高级</summary><label className="inline-label">同时下载的文件数<input type="number" min="1" max="16" disabled={locked} value={concurrency} onChange={e => setConcurrency(Number(e.target.value))} /></label></details></div></div>
      {batches.length > 0 && <><div className="batch-overview"><div><span>全部批次</span><strong>{formatCount(batches.length)}</strong></div><div><span>已下载或导入</span><strong>{formatCount(finishedCount)}</strong></div><div><span>尚未完成</span><strong>{formatCount(batches.length - finishedCount)}</strong></div><div><span>失败批次</span><strong>{formatCount(failedCount)}</strong></div></div><div className="batch-toolbar"><label>筛选批次<select value={batchFilter} onChange={e => { setBatchFilter(e.target.value); setBatchPage(1) }}><option value="all">全部批次</option><option value="pending">尚未完成</option><option value="finished">已下载或导入</option><option value="failed">失败批次</option></select></label><span>显示 {filteredBatches.length ? (page - 1) * PAGE_SIZE + 1 : 0}–{Math.min(page * PAGE_SIZE, filteredBatches.length)} / {formatCount(filteredBatches.length)} 批</span></div></>}
      {batches.length ? <><div className="batch-grid">{filteredBatches.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE).map(batch => {
        const p = progress[batch.id]; const total = p?.totalBytes ?? batch.plannedBytes; const state = p?.batch.status ?? batch.status; const finished = ['READY', 'IMPORTED'].includes(state); const done = p?.downloadedBytes ?? (finished ? total : 0); const pct = finished ? 100 : total ? Math.min(100, Math.max(0, done / total * 100)) : 0; const active = running.has(batch.id); const rate = active ? rates[batch.id] ?? 0 : 0
        return <article className="batch-card" key={batch.id}><div className="batch-head"><div><strong>第 {batch.sequence} 批</strong><small>{shortID(batch.id)}</small></div><span className={`status status-${state.toLowerCase()}`}>{statusLabel(state)}</span></div><div className="batch-stats"><span>{formatBytes(batch.plannedBytes)}</span><span>{formatCount(batch.objectCount)} 个文件</span><span>{batch.packCount} 个文件包</span></div><div className="progress" role="progressbar" aria-label={`第 ${batch.sequence} 批下载进度`} aria-valuenow={Math.round(pct)} aria-valuemin={0} aria-valuemax={100}><div style={{width: `${pct}%`}} /></div><small>{formatBytes(done)} / {formatBytes(total)} · {pct.toFixed(1)}%{p ? ` · ${p.verifiedEntries}/${p.totalEntries} 个文件已检查` : ''}</small>{active && <div className="batch-telemetry"><span>{rate > 0 ? `${formatBytes(rate)}/s` : '等待新的下载进度'}</span><span>{rate > 0 && total > done ? `预计剩余 ${duration((total - done) / rate)}` : '剩余时间待估算'}</span></div>}{(p?.batch.errorText || batch.errorText) && <details className="advanced-options"><summary>查看错误详情</summary><p className="error-detail">{p?.batch.errorText || batch.errorText}</p></details>}<div className="button-row"><button onClick={() => refreshBatchProgress(batch.id)}>刷新</button>{active ? <button onClick={() => pause(batch.id)}>暂停</button> : (p?.batch.status ?? batch.status) === 'READY' ? <button disabled={!historyReady || locked} onClick={() => redownload(batch)}>重新下载这一批</button> : <button className="primary" disabled={!historyReady || locked || state === 'IMPORTED'} onClick={() => download(batch)}>下载 / 继续</button>}</div></article>
      })}</div>{!filteredBatches.length && <p className="empty">没有符合条件的批次。</p>}{maxPage > 1 && <div className="batch-pagination"><button disabled={page <= 1} onClick={() => setBatchPage(page - 1)}>上一页</button><span>第 {page} / {maxPage} 页</span><button disabled={page >= maxPage} onClick={() => setBatchPage(page + 1)}>下一页</button></div>}<p className="small telemetry-note">速度与剩余时间根据最近的已下载字节数变化估算，不包含后续校验及打包时间。</p></> : <div className="empty panel-empty">先打开状态文件、选好软件源，再点击“查看需要下载的更新”。</div>}
    </div>

    <details className="secondary-section"><summary>以前的下载任务（需要继续时再打开）</summary><div className="secondary-section-body"><div className="card table-card"><div className="card-title"><span>以前的下载任务</span><button disabled={!historyReady || locked} onClick={async () => { try { setHistory(await app().ListEpochs(capsuleID, sourceID)) } catch (e) { setMessage(errorText(e)) } }}>刷新</button></div><div className="table-wrap"><table><thead><tr><th>任务编号</th><th>状态</th><th>版本变化</th><th>数据量</th><th>批次</th><th>创建时间</th><th></th></tr></thead><tbody>{history.length ? history.map(e => <tr key={e.id}><td><code>{shortID(e.id)}</code></td><td><span className={`status status-${e.status.toLowerCase()}`}>{statusLabel(e.status)}</span></td><td><small className="mono clip">{e.baseCursor.value} → {e.targetCursor.value}</small></td><td>{formatBytes(e.totalBytes)}<small>{formatCount(e.totalObjects)} 个文件</small></td><td>{e.totalBatches}</td><td>{formatDate(e.createdAt)}</td><td><button disabled={locked} onClick={() => resume(e.id)}>查看任务</button></td></tr>) : <tr><td colSpan={7} className="empty">还没有以前的下载任务。</td></tr>}</tbody></table></div></div></div></details>
  </section>
}
