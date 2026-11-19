import { useEffect, useMemo, useState } from 'react'
import { app } from '../backend'
import { errorText, formatBytes, formatCount, formatDate, shortID, statusLabel } from '../format'
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
      setSelectedJobID(job.id); setCandidates(null); setMessage(`已开始重新清点文件。任务：${shortID(job.id)}`)
      await refreshJobs()
    } catch (e) { setMessage(errorText(e)) }
    finally { setBusy(false) }
  }

  async function startGC(execute: boolean) {
    if (!sourceID) return
    if (execute) {
      if (!selectedJob || selectedJob.kind !== 'GC' || selectedJob.status !== 'COMPLETED' || selectedJob.execute) {
        setMessage('请先查看可清理的文件，确认列表后再执行删除。')
        return
      }
      const count = candidates?.stats.objects ?? selectedJob.candidateObjects
      const bytes = candidates?.stats.bytes ?? selectedJob.candidateBytes
      const ok = window.confirm(`系统将重新检查并删除服务器中不再使用的文件，删除后无法恢复。\n上次预览有 ${formatCount(count)} 个文件，共 ${formatBytes(bytes)}。\n\n确定要删除吗？`)
      if (!ok) return
    }
    setBusy(true); setMessage('')
    try {
      const job = await app().StartGC(sourceID, execute)
      setSelectedJobID(job.id); setCandidates(null)
      setMessage(execute ? `已开始重新检查并清理文件。任务：${shortID(job.id)}` : `正在查找可能不再使用的文件，不会直接删除。任务：${shortID(job.id)}`)
      await refreshJobs()
    } catch (e) { setMessage(errorText(e)) }
    finally { setBusy(false) }
  }

  async function cancelJob() {
    if (!activeJob) return
    setBusy(true); setMessage('')
    try {
      await app().CancelMaintenance(activeJob.id)
      setMessage(`已请求停止任务：${shortID(activeJob.id)}`)
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
      <div className="card-title"><span>维护工具</span><span>检查文件和清理服务器空间</span></div>
      <div className="form-grid">
        <label>软件源<select value={sourceID} onChange={e => setSourceID(e.target.value)} disabled={!overview || busy}>
          {(overview?.sources ?? []).map(x => <option key={x.source.id} value={x.source.id}>{x.source.name} · {x.source.type}/{x.source.provider}</option>)}
        </select></label>
        <div className="span-2"><p>如果服务器里的文件数量不对，可以先重新清点。需要腾出空间时，先看看哪些文件可能不再使用，再决定是否删除。</p>{source?.source.type === 'apt' && <small>APT 的共享包目录默认不会被清理。只有明确设为该软件源独占时，才允许删除其中的废弃文件。</small>}</div>
        <div className="button-row align-end">
          <button disabled={!sourceID || !!activeJob || busy} onClick={startInventory}>重新清点</button>
          <button disabled={!sourceID || !!activeJob || busy} onClick={() => startGC(false)}>查看可清理文件</button>
          {activeJob && <button className="danger-ghost" disabled={busy} onClick={cancelJob}>取消任务</button>}
          <button disabled={!sourceID || busy} onClick={refreshJobs}>刷新</button>
        </div>
      </div>
    </div>

    <div className="card table-card">
      <div className="card-title"><span>维护记录</span><span>{jobs.length ? `${jobs.length} 条` : '暂无记录'}</span></div>
      <div className="table-wrap"><table><thead><tr><th>任务</th><th>类型</th><th>状态</th><th>扫描</th><th>候选</th><th>实际删除</th><th>时间</th><th></th></tr></thead>
        <tbody>{jobs.length ? jobs.map(job => <tr key={job.id}>
          <td><code>{shortID(job.id)}</code>{job.errorText && <small className="clip">{job.errorText}</small>}</td>
          <td>{job.kind === 'GC' ? '清理文件' : '重新清点'}{job.kind === 'GC' && <small>{job.execute ? '执行删除' : '仅预览'}</small>}</td>
          <td><span className={statusClass(job.status)}>{statusLabel(job.status)}</span></td>
          <td>{formatCount(job.scannedObjects)}<small>{formatBytes(job.scannedBytes)}</small></td>
          <td>{job.kind === 'GC' ? <>{formatCount(job.candidateObjects)}<small>{formatBytes(job.candidateBytes)}</small></> : '—'}</td>
          <td>{job.kind === 'GC' && job.execute ? <>{formatCount(job.affectedObjects)}<small>{formatBytes(job.affectedBytes)}</small></> : '—'}</td>
          <td>{formatDate(job.createdAt)}<small>{job.completedAt ? `完成 ${formatDate(job.completedAt)}` : job.startedAt ? `开始 ${formatDate(job.startedAt)}` : ''}</small></td>
          <td><div className="button-row">{job.kind === 'GC' && terminal(job) && job.tempCatalogPath && <button disabled={busy} onClick={() => showCandidates(job)}>查看列表</button>}{job.kind === 'GC' && job.status === 'COMPLETED' && !job.execute && <button className="danger-ghost" disabled={busy || !!activeJob} onClick={() => { setSelectedJobID(job.id); void showCandidates(job).then(() => undefined) }}>选中本次预览</button>}</div></td>
        </tr>) : <tr><td colSpan={8} className="empty">选择软件源后，可以查看以前的检查和清理记录。</td></tr>}</tbody>
      </table></div>
    </div>

    {selectedJob?.kind === 'GC' && selectedJob.status === 'COMPLETED' && !selectedJob.execute && <div className="card">
      <div className="card-title"><span>确认清理</span><span>{shortID(selectedJob.id)}</span></div>
      <div className="button-row"><button className="danger-ghost" disabled={busy || !!activeJob} onClick={() => startGC(true)}>重新检查并永久删除</button><p>删除前会再次检查文件是否还被仓库使用，不会直接按旧清单删除。</p></div>
    </div>}

    {candidates && <div className="card table-card">
      <div className="card-title"><span>可能不再需要的文件</span><span>{formatCount(candidates.stats.objects)} · {formatBytes(candidates.stats.bytes)}</span></div>
      <div className="table-wrap"><table><thead><tr><th>文件路径</th><th>大小</th><th>扫描时修改时间</th></tr></thead><tbody>
        {candidates.items.length ? candidates.items.map(item => <tr key={item.logicalPath}><td><code>{item.logicalPath}</code></td><td>{formatBytes(item.size)}</td><td>{new Date(item.modTimeUnixNano / 1_000_000).toLocaleString()}</td></tr>) : <tr><td colSpan={3} className="empty">没有发现需要清理的文件。</td></tr>}
      </tbody></table></div>
      {candidates.next && <div className="button-row" style={{ padding: 14 }}><button disabled={busy || !selectedJob} onClick={() => selectedJob && showCandidates(selectedJob, true)}>加载更多</button></div>}
    </div>}
    {message && <div className="notice">{message}</div>}
  </>
}
