import { useEffect, useMemo, useState } from 'react'
import { app } from '../backend'
import { errorText, formatBytes, formatCount, formatDate, shortID } from '../format'
import type { AgentOverview, GCCandidatesPage, MaintenanceJob } from '../types'

type Props = { overview: AgentOverview | null }

function statusClass(status: string) {
  switch (status) {
    case 'COMPLETED': return 'status status-complete'
    case 'FAILED': return 'status status-failed'
    case 'CANCELLED': return 'status status-cancelled'
    case 'RUNNING': return 'status status-transferring'
    default: return 'status'
  }
}

function terminal(job: MaintenanceJob) {
  return job.status === 'COMPLETED' || job.status === 'FAILED' || job.status === 'CANCELLED'
}

export function MaintenancePanel({ overview }: Props) {
  const [sourceID, setSourceID] = useState('')
  const [jobs, setJobs] = useState<MaintenanceJob[]>([])
  const [selectedJobID, setSelectedJobID] = useState('')
  const [candidates, setCandidates] = useState<GCCandidatesPage | null>(null)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')

  const source = useMemo(() => overview?.sources.find(x => x.source.id === sourceID), [overview, sourceID])
  const selectedJob = useMemo(() => jobs.find(x => x.id === selectedJobID), [jobs, selectedJobID])
  const activeJob = useMemo(() => jobs.find(x => x.status === 'QUEUED' || x.status === 'RUNNING'), [jobs])

  useEffect(() => {
    const sources = overview?.sources ?? []
    if (!sources.length) {
      setSourceID(''); setJobs([]); setSelectedJobID(''); setCandidates(null)
      return
    }
    if (!sources.some(x => x.source.id === sourceID)) setSourceID(sources[0].source.id)
  }, [overview, sourceID])

  useEffect(() => {
    if (!overview || !sourceID) return
    let disposed = false
    async function refresh() {
      try {
        const value = await app().ListMaintenance(sourceID, 50)
        if (!disposed) setJobs(value ?? [])
      } catch (e) {
        if (!disposed) setMessage(errorText(e))
      }
    }
    void refresh()
    const timer = window.setInterval(refresh, 3000)
    return () => { disposed = true; window.clearInterval(timer) }
  }, [overview, sourceID])

  useEffect(() => {
    setSelectedJobID(''); setCandidates(null); setMessage('')
  }, [sourceID])

  async function refreshJobs() {
    if (!sourceID) return
    setBusy(true); setMessage('')
    try { setJobs(await app().ListMaintenance(sourceID, 50) ?? []) }
    catch (e) { setMessage(errorText(e)) }
    finally { setBusy(false) }
  }

  async function startInventory() {
    if (!sourceID) return
    setBusy(true); setMessage('')
    try {
      const job = await app().StartInventory(sourceID)
      setSelectedJobID(job.id); setCandidates(null); setMessage(`Inventory 已创建：${shortID(job.id)}`)
      await refreshJobs()
    } catch (e) { setMessage(errorText(e)) }
    finally { setBusy(false) }
  }

  async function startGC(execute: boolean) {
    if (!sourceID) return
    if (execute) {
      if (!selectedJob || selectedJob.kind !== 'GC' || selectedJob.status !== 'COMPLETED' || selectedJob.execute) {
        setMessage('必须先选择一个已完成的 GC 预览任务。')
        return
      }
      const count = candidates?.stats.objects ?? selectedJob.candidateObjects
      const bytes = candidates?.stats.bytes ?? selectedJob.candidateBytes
      const ok = window.confirm(`将重新扫描并物理删除当前仍未被 Catalog 引用的文件。\n上次预览：${formatCount(count)} 个对象 / ${formatBytes(bytes)}。\n\n确定继续？`)
      if (!ok) return
    }
    setBusy(true); setMessage('')
    try {
      const job = await app().StartGC(sourceID, execute)
      setSelectedJobID(job.id); setCandidates(null)
      setMessage(execute ? `物理 GC 已创建：${shortID(job.id)}` : `GC 预览已创建：${shortID(job.id)}`)
      await refreshJobs()
    } catch (e) { setMessage(errorText(e)) }
    finally { setBusy(false) }
  }

  async function cancelJob() {
    if (!activeJob) return
    setBusy(true); setMessage('')
    try {
      await app().CancelMaintenance(activeJob.id)
      setMessage(`已请求取消：${shortID(activeJob.id)}`)
      await refreshJobs()
    } catch (e) { setMessage(errorText(e)) }
    finally { setBusy(false) }
  }

  async function showCandidates(job: MaintenanceJob, append = false) {
    if (job.kind !== 'GC' || !terminal(job)) return
    setBusy(true); setMessage('')
    try {
      const after = append && selectedJobID === job.id ? candidates?.next ?? '' : ''
      const page = await app().GCCandidates(job.id, after, 100)
      setSelectedJobID(job.id)
      if (append && candidates && selectedJobID === job.id) {
        setCandidates({ ...page, items: [...candidates.items, ...page.items] })
      } else {
        setCandidates(page)
      }
    } catch (e) { setMessage(errorText(e)) }
    finally { setBusy(false) }
  }

  return <>
    <div className="card">
      <div className="card-title"><span>维护 / Reconcile</span><span>Inventory 与物理 GC 均在内网 Agent 执行</span></div>
      <div className="form-grid">
        <label>Source<select value={sourceID} onChange={e => setSourceID(e.target.value)} disabled={!overview || busy}>
          {(overview?.sources ?? []).map(x => <option key={x.source.id} value={x.source.id}>{x.source.name} · {x.source.type}/{x.source.provider}</option>)}
        </select></label>
        <div className="span-2"><p>Inventory 在独立临时 Catalog 中完整重建，成功后才原子替换 live Catalog。GC 默认只生成候选，不删除文件。</p>{source?.source.type === 'apt' && <small>APT 默认不清理共享 pool/；仅当 Source config 设置 gcOwnsPool=true 时，pool 才属于物理 GC 范围。</small>}</div>
        <div className="button-row align-end">
          <button disabled={!sourceID || !!activeJob || busy} onClick={startInventory}>重新清点</button>
          <button disabled={!sourceID || !!activeJob || busy} onClick={() => startGC(false)}>GC 预览</button>
          {activeJob && <button className="danger-ghost" disabled={busy} onClick={cancelJob}>取消任务</button>}
          <button disabled={!sourceID || busy} onClick={refreshJobs}>刷新</button>
        </div>
      </div>
    </div>

    <div className="card table-card">
      <div className="card-title"><span>Maintenance Jobs</span><span>{jobs.length ? `${jobs.length} 条` : '暂无记录'}</span></div>
      <div className="table-wrap"><table><thead><tr><th>任务</th><th>类型</th><th>状态</th><th>扫描</th><th>候选</th><th>实际删除</th><th>时间</th><th></th></tr></thead>
        <tbody>{jobs.length ? jobs.map(job => <tr key={job.id}>
          <td><code>{shortID(job.id)}</code>{job.errorText && <small className="clip">{job.errorText}</small>}</td>
          <td>{job.kind}{job.kind === 'GC' && <small>{job.execute ? 'physical execute' : 'dry-run'}</small>}</td>
          <td><span className={statusClass(job.status)}>{job.status}</span></td>
          <td>{formatCount(job.scannedObjects)}<small>{formatBytes(job.scannedBytes)}</small></td>
          <td>{job.kind === 'GC' ? <>{formatCount(job.candidateObjects)}<small>{formatBytes(job.candidateBytes)}</small></> : '—'}</td>
          <td>{job.kind === 'GC' && job.execute ? <>{formatCount(job.affectedObjects)}<small>{formatBytes(job.affectedBytes)}</small></> : '—'}</td>
          <td>{formatDate(job.createdAt)}<small>{job.completedAt ? `完成 ${formatDate(job.completedAt)}` : job.startedAt ? `开始 ${formatDate(job.startedAt)}` : ''}</small></td>
          <td><div className="button-row">{job.kind === 'GC' && terminal(job) && job.tempCatalogPath && <button disabled={busy} onClick={() => showCandidates(job)}>候选</button>}{job.kind === 'GC' && job.status === 'COMPLETED' && !job.execute && <button className="danger-ghost" disabled={busy || !!activeJob} onClick={() => { setSelectedJobID(job.id); void showCandidates(job).then(() => undefined) }}>选择预览</button>}</div></td>
        </tr>) : <tr><td colSpan={8} className="empty">选择 Source 后显示维护任务。</td></tr>}</tbody>
      </table></div>
    </div>

    {selectedJob?.kind === 'GC' && selectedJob.status === 'COMPLETED' && !selectedJob.execute && <div className="card">
      <div className="card-title"><span>GC 预览操作</span><span>{shortID(selectedJob.id)}</span></div>
      <div className="button-row"><button className="danger-ghost" disabled={busy || !!activeJob} onClick={() => startGC(true)}>重新扫描并执行物理删除</button><p>执行任务不会直接复用旧候选；Agent 会重新扫描、重新检查 Catalog/mtime/size，再逐文件安全删除。</p></div>
    </div>}

    {candidates && <div className="card table-card">
      <div className="card-title"><span>GC Candidates</span><span>{formatCount(candidates.stats.objects)} · {formatBytes(candidates.stats.bytes)}</span></div>
      <div className="table-wrap"><table><thead><tr><th>Logical Path</th><th>Size</th><th>扫描时修改时间</th></tr></thead><tbody>
        {candidates.items.length ? candidates.items.map(item => <tr key={item.logicalPath}><td><code>{item.logicalPath}</code></td><td>{formatBytes(item.size)}</td><td>{new Date(item.modTimeUnixNano / 1_000_000).toLocaleString()}</td></tr>) : <tr><td colSpan={3} className="empty">没有 GC 候选。</td></tr>}
      </tbody></table></div>
      {candidates.next && <div className="button-row" style={{ padding: 14 }}><button disabled={busy || !selectedJob} onClick={() => selectedJob && showCandidates(selectedJob, true)}>加载更多</button></div>}
    </div>}
    {message && <div className="notice">{message}</div>}
  </>
}
