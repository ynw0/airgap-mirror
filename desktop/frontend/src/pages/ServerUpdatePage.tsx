import { useEffect, useMemo, useRef, useState } from 'react'
import { app, onEvent } from '../backend'
import { errorText, formatBytes, formatCount, shortID, statusLabel } from '../format'
import type { AgentOverview, DesktopTransferResult, TransferProgress, ValidatedBundle } from '../types'

type Props = { overview: AgentOverview | null }

export function ServerUpdatePage({ overview }: Props) {
  const [path, setPath] = useState('')
  const [bundle, setBundle] = useState<ValidatedBundle | null>(null)
  const [requestID, setRequestID] = useState('')
  const [chunkMiB, setChunkMiB] = useState(64)
  const [progress, setProgress] = useState<TransferProgress | null>(null)
  const sample = useRef<{key: string; bytes: number; at: number; rate: number} | null>(null)
  const [rate, setRate] = useState(0)
  const [observed, setObserved] = useState<Record<string, number>>({})
  const [committed, setCommitted] = useState<Set<string>>(new Set())
  const [pauseRequested, setPauseRequested] = useState(false)
  const [result, setResult] = useState<DesktopTransferResult | null>(null)
  const [running, setRunning] = useState(false)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')

  useEffect(() => onEvent<TransferProgress>('transfer-progress', p => {
    if (!p) return
    const key = p.packId || 'manifest', now = performance.now(), prior = sample.current
    const elapsed = prior && prior.key === key ? (now - prior.at) / 1000 : 0
    const delta = prior && prior.key === key ? p.transferred - prior.bytes : 0
    const instant = elapsed > 0 && delta > 0 && p.kind !== 'pack-committed' ? delta / elapsed : 0
    const nextRate = instant > 0 && prior?.rate ? prior.rate * .6 + instant * .4 : instant
    sample.current = { key, bytes: p.transferred, at: now, rate: nextRate }
    setRate(nextRate); setProgress(p)
    setObserved(prev => ({ ...prev, [key]: Math.max(0, Math.min(p.total, p.transferred)) }))
    if (p.kind === 'pack-committed' && p.packId) setCommitted(prev => new Set(prev).add(p.packId!))
  }), [])
  useEffect(() => {
    if (!running) return
    const timer = window.setInterval(() => { if (sample.current && performance.now() - sample.current.at > 5000) setRate(0) }, 1000)
    return () => window.clearInterval(timer)
  }, [running])
  function resetTelemetry() { sample.current = null; setRate(0); setObserved({}); setCommitted(new Set()); setProgress(null); setPauseRequested(false) }
  const pct = useMemo(() => progress?.total ? Math.min(100, progress.transferred / progress.total * 100) : 0, [progress])

  async function choose() {
    const value = await app().PickBatchDirectory()
    if (value) { setPath(value); setBundle(null); setResult(null); resetTelemetry() }
  }
  async function validate() {
    if (!path) return
    setBusy(true); setMessage(''); setResult(null)
    try { const value = await app().ValidateBatchDirectory(path); setBundle(value); setMessage('文件清单检查完成，可以开始导入。上传时还会核对文件内容。') }
    catch (e) { setBundle(null); setMessage(errorText(e)) }
    finally { setBusy(false) }
  }
  async function transfer() {
    if (!path) { setMessage('请先选择下载完成的批次文件夹。'); return }
    if (!overview?.connected) { setMessage('请先在“连接服务器”页面连接内网服务器。'); return }
    if (!Number.isInteger(chunkMiB) || chunkMiB < 1 || chunkMiB > 128) { setMessage('单次上传大小必须为 1 至 128 MiB 的整数。'); return }
    setRunning(true); setMessage(''); setResult(null); resetTelemetry()
    try {
      if (!bundle) {
        const checked = await app().ValidateBatchDirectory(path)
        setBundle(checked)
      }
      const value = await app().TransferBatchDirectory(path, { chunkSize: Math.round(chunkMiB * 1024 * 1024), requestId: requestID.trim() || undefined })
      setResult(value)
      setMessage(value.workspaceTracked ? '这批文件已导入服务器，本机的下载记录也已更新。' : '这批文件已导入服务器，即使是在另一台电脑下载的，也能正常使用。')
    } catch (e) { setMessage(errorText(e)) }
    finally { setRunning(false); setRate(0); setPauseRequested(false) }
  }
  async function pause() {
    if (!bundle || pauseRequested) return
    try { await app().PauseTransfer(bundle.descriptor.batchId); setPauseRequested(true); setMessage('已请求暂停，等待当前传输结束。') } catch (e) { setMessage(errorText(e)) }
  }

  const d = bundle?.descriptor
  const expectedBytes = d ? d.manifestSize + d.packs.reduce((n, p) => n + p.size, 0) : 0
  const observedBytes = Math.min(expectedBytes, Object.values(observed).reduce((n, size) => n + size, 0))
  const overallPct = result ? 100 : expectedBytes > 0 ? observedBytes / expectedBytes * 100 : 0
  const remaining = progress && rate > 0 ? Math.ceil(Math.max(0, progress.total - progress.transferred) / rate) : null
  return <section className="page-stack">
    <div className="page-header"><div><h2>把下载好的文件带回内网</h2><p>接上移动硬盘，选中下载完成的批次文件夹。检查通过后就可以导入服务器；中途断开也能继续。</p></div><span className={`connection ${overview?.connected ? 'ok' : ''}`}>{overview?.connected ? `已连接 ${overview.baseUrl}` : '还没有连接服务器'}</span></div>

    {message && <div className="notice" role="status" aria-live="polite">{message}</div>}
    {!overview?.connected && <div className="card guidance">尚未连接内网服务器。可以先检查本地文件；导入前请在“连接服务器”页面建立连接。</div>}
    <div className="card form-grid transfer-form">
      <label className="span-2">要导入的文件夹<div className="path-picker"><input disabled={busy || running} value={path} onChange={e => { setPath(e.target.value); setBundle(null); setResult(null); setProgress(null) }} placeholder="选择下载完成的“第 X 批”文件夹" /><button disabled={busy || running} onClick={choose}>浏览</button></div></label>
      <details className="advanced-options span-2"><summary>高级选项（默认无需修改）</summary><div className="form-grid">
        <label>单次上传大小 (MiB)<input type="number" min="1" max="128" disabled={busy || running} value={chunkMiB} onChange={e => setChunkMiB(Number(e.target.value))} /></label>
        <label>请求标识（可选）<input disabled={busy || running} value={requestID} onChange={e => setRequestID(e.target.value)} placeholder="自动生成" /></label>
      </div></details>
      <div className="button-row align-end"><button disabled={!path || busy || running} onClick={validate}>只检查文件</button>{running ? <button disabled={!bundle || pauseRequested} onClick={pause}>{pauseRequested ? '正在暂停…' : '暂停导入'}</button> : <button className="primary" disabled={!path || busy || !overview?.connected} onClick={transfer}>{progress && !result ? '继续导入' : '检查并导入服务器'}</button>}</div>
    </div>

    {d ? <>
      <div className="metrics-grid">
        <div className="metric"><span>批次</span><strong>#{d.batchSequence} / {d.epochTotalBatches}</strong><small>{shortID(d.batchId)}</small></div>
        <div className="metric"><span>数据量</span><strong>{formatBytes(d.batchBytes)}</strong><small>{formatCount(d.batchObjects)} 个文件</small></div>
        <div className="metric"><span>文件包</span><strong>{d.packs.length}</strong><small>文件清单 {formatBytes(d.manifestSize)}</small></div>
        <div className="metric"><span>同步任务</span><strong>{shortID(d.epochId)}</strong><small>{d.baseCursor.kind} → {d.targetCursor.kind}</small></div>
      </div>
      <div className="card table-card"><div className="card-title"><span>文件包清单</span><span>内容在上传时核对</span></div><div className="table-wrap"><table><thead><tr><th>#</th><th>文件包编号</th><th>文件</th><th>大小</th><th>文件数</th><th>SHA-256</th></tr></thead><tbody>{d.packs.map(p => <tr key={p.id}><td>{p.sequence}</td><td><code>{shortID(p.id)}</code></td><td className="mono">{p.file}</td><td>{formatBytes(p.size)}</td><td>{formatCount(p.entryCount)}</td><td><small className="mono clip">{p.sha256}</small></td></tr>)}</tbody></table></div></div>
    </> : <div className="card empty panel-empty">先选好移动硬盘上的批次文件夹，再点击“检查并导入服务器”。程序会先检查文件，确认没有问题后再上传。</div>}

    {(running || progress || result) && <div className="card current-task"><div className="card-title"><span>本批次导入进度</span><span>{result ? '已导入' : pauseRequested ? '正在暂停' : running ? '进行中' : '传输已停止'}</span></div>{d && <><div className="batch-overview"><div><span>服务器已报告的数据</span><strong>{formatBytes(result ? expectedBytes : observedBytes)}</strong></div><div><span>文件清单及文件包总大小</span><strong>{formatBytes(expectedBytes)}</strong></div><div><span>本次确认导入的文件包</span><strong>{result ? d.packs.length : committed.size} / {d.packs.length}</strong></div></div><div className="progress" role="progressbar" aria-label="已收到进度报告的批次数据" aria-valuenow={Math.round(overallPct)} aria-valuemin={0} aria-valuemax={100}><div style={{width: `${overallPct}%`}} /></div><p className="small">{result ? '服务器已确认本批次导入完成。' : '仅统计本次收到进度报告的数据；恢复导入时，之前已导入且被跳过的文件包不计入此进度。上传完成不代表导入完成。'}</p></>}{running && !progress && <p>正在检查本地批次及服务器导入条件……</p>}</div>}
    {progress && <div className="card progress-card"><div className="card-title"><span>当前传输进度</span><span>{progress.packId ? `文件包 ${shortID(progress.packId)}` : '文件清单'}</span></div><div className="progress" role="progressbar" aria-label="当前文件传输进度" aria-valuenow={Math.round(pct)} aria-valuemin={0} aria-valuemax={100}><div style={{width: `${pct}%`}} /></div><div className="progress-caption"><span>{formatBytes(progress.transferred)} / {formatBytes(progress.total)}</span><strong>{pct.toFixed(1)}%</strong></div>{running && <div className="batch-telemetry"><span>{rate > 0 ? `估算上传速度 ${formatBytes(rate)}/s` : '等待新的上传进度'}</span><span>{remaining !== null && remaining > 0 ? `当前文件预计剩余 ${remaining >= 60 ? Math.ceil(remaining / 60) + ' 分钟' : remaining + ' 秒'}` : '剩余时间待估算'}</span></div>}</div>}

    {result && <div className="card success-card"><h3>这批文件已经导入服务器</h3><p>{result.workspaceTracked ? '本机的下载记录也已更新。如果还有其他批次，继续选择下一批即可。' : '如果还有其他批次，继续选择下一批即可。'}</p><code>{statusLabel(result.local?.remote.status.session.status ?? result.standalone?.status.session.status ?? 'BATCH_IMPORTED')}</code></div>}
  </section>
}
