import { useEffect, useMemo, useState } from 'react'
import { app, onEvent } from '../backend'
import { errorText, formatBytes, formatCount, shortID } from '../format'
import type { AgentOverview, DesktopTransferResult, TransferProgress, ValidatedBundle } from '../types'

type Props = { overview: AgentOverview | null }

export function ServerUpdatePage({ overview }: Props) {
  const [path, setPath] = useState('')
  const [bundle, setBundle] = useState<ValidatedBundle | null>(null)
  const [requestID, setRequestID] = useState('')
  const [chunkMiB, setChunkMiB] = useState(64)
  const [progress, setProgress] = useState<TransferProgress | null>(null)
  const [result, setResult] = useState<DesktopTransferResult | null>(null)
  const [running, setRunning] = useState(false)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')

  useEffect(() => onEvent<TransferProgress>('transfer-progress', p => p && setProgress(p)), [])
  const pct = useMemo(() => progress?.total ? Math.min(100, progress.transferred / progress.total * 100) : 0, [progress])

  async function choose() {
    const value = await app().PickBatchDirectory()
    if (value) { setPath(value); setBundle(null); setResult(null); setProgress(null) }
  }
  async function validate() {
    if (!path) return
    setBusy(true); setMessage(''); setResult(null)
    try { const value = await app().ValidateBatchDirectory(path); setBundle(value); setMessage('Bundle 本地一致性校验通过。') }
    catch (e) { setBundle(null); setMessage(errorText(e)) }
    finally { setBusy(false) }
  }
  async function transfer() {
    if (!bundle) { await validate(); return }
    if (!overview?.connected) { setMessage('请先在“源状态”页面连接内网 Mirror Agent。'); return }
    setRunning(true); setMessage(''); setResult(null)
    try {
      const value = await app().TransferBatchDirectory(path, { chunkSize: Math.round(chunkMiB * 1024 * 1024), requestId: requestID.trim() || undefined })
      setResult(value)
      setMessage(value.workspaceTracked ? 'Batch 已导入，Workspace 状态已与服务器对账。' : 'Batch 已以独立介质模式导入服务器。')
    } catch (e) { setMessage(errorText(e)) }
    finally { setRunning(false) }
  }
  async function pause() {
    if (!bundle) return
    try { await app().PauseTransfer(bundle.descriptor.batchId) } catch (e) { setMessage(errorText(e)) }
  }

  const d = bundle?.descriptor
  return <section className="page-stack">
    <div className="page-header"><div><h2>服务器更新</h2><p>验证移动介质 Batch，断点上传到 Mirror Agent，逐 Pack commit 后触发 Publish。</p></div><span className={`connection ${overview?.connected ? 'ok' : ''}`}>{overview?.connected ? `Agent · ${overview.baseUrl}` : 'Agent 未连接'}</span></div>

    <div className="card form-grid transfer-form">
      <label className="span-2">Batch 目录<div className="path-picker"><input value={path} onChange={e => { setPath(e.target.value); setBundle(null) }} placeholder="选择包含 batch.json / manifest.sqlite / packs 的目录" /><button onClick={choose}>浏览</button></div></label>
      <label>上传 Chunk (MiB)<input type="number" min="1" max="128" value={chunkMiB} onChange={e => setChunkMiB(Number(e.target.value))} /></label>
      <label>Request ID（可选）<input value={requestID} onChange={e => setRequestID(e.target.value)} placeholder="默认 batch-<id>" /></label>
      <div className="button-row align-end"><button disabled={!path || busy || running} onClick={validate}>本地校验</button>{running ? <button onClick={pause}>暂停</button> : <button className="primary" disabled={!bundle || !overview?.connected} onClick={transfer}>上传 / 继续</button>}</div>
    </div>

    {d ? <>
      <div className="metrics-grid">
        <div className="metric"><span>Batch</span><strong>#{d.batchSequence} / {d.epochTotalBatches}</strong><small>{shortID(d.batchId)}</small></div>
        <div className="metric"><span>数据量</span><strong>{formatBytes(d.batchBytes)}</strong><small>{formatCount(d.batchObjects)} objects</small></div>
        <div className="metric"><span>Packs</span><strong>{d.packs.length}</strong><small>manifest {formatBytes(d.manifestSize)}</small></div>
        <div className="metric"><span>Epoch</span><strong>{shortID(d.epochId)}</strong><small>{d.baseCursor.kind} → {d.targetCursor.kind}</small></div>
      </div>
      <div className="card table-card"><div className="card-title"><span>Pack 清单</span><span>本地 Header / Manifest 已校验</span></div><div className="table-wrap"><table><thead><tr><th>#</th><th>Pack ID</th><th>文件</th><th>大小</th><th>Entries</th><th>SHA-256</th></tr></thead><tbody>{d.packs.map(p => <tr key={p.id}><td>{p.sequence}</td><td><code>{shortID(p.id)}</code></td><td className="mono">{p.file}</td><td>{formatBytes(p.size)}</td><td>{formatCount(p.entryCount)}</td><td><small className="mono clip">{p.sha256}</small></td></tr>)}</tbody></table></div></div>
    </> : <div className="card empty panel-empty">选择 Batch 目录并执行本地校验。</div>}

    {progress && <div className="card progress-card"><div className="card-title"><span>{progress.kind}</span><span>{progress.packId ? shortID(progress.packId) : 'manifest'}</span></div><div className="progress"><div style={{width: `${pct}%`}} /></div><div className="progress-caption"><span>{formatBytes(progress.transferred)} / {formatBytes(progress.total)}</span><strong>{pct.toFixed(1)}%</strong></div></div>}

    {result && <div className="card success-card"><h3>服务器导入完成</h3><p>{result.workspaceTracked ? '该 Batch 属于当前 Workspace；本地 Batch/Epoch 已按服务器真实状态推进。' : '该 Bundle 未在本机 Workspace 中登记，已按自描述 Bundle 独立导入。'}</p><code>{result.local?.remote.status.session.status ?? result.standalone?.status.session.status ?? 'BATCH_IMPORTED'}</code></div>}
    {message && <div className="notice">{message}</div>}
  </section>
}
