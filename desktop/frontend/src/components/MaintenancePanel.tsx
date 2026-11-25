import { useEffect, useMemo, useState } from 'react'
import { app, onEvent } from '../backend'
import { errorText, formatBytes, formatCount, formatDate, shortID, statusLabel } from '../format'
import type { AgentOverview, GCCandidatesPage, MaintenanceJob, RepairIssuesPage, ExportJob } from '../types'

type Props = { overview: AgentOverview | null }

function statusClass(status: string) {
  switch (status) {
    case 'COMPLETED': return 'status status-complete'
    case 'FAILED': return 'status status-failed'
    case 'CANCELLED': return 'status status-cancelled'
    case 'RUNNING': return 'status status-transferring'
    case 'PAUSED': return 'status status-paused'
    default: return 'status'
  }
}

function taskName(job: MaintenanceJob) {
  return job.kind === 'GC' ? (job.execute ? '清理文件' : '查找可清理的文件') : job.kind === 'REPAIR' ? (job.hashMiBPerSecond ? 'SHA256 完整校验' : '检查缺失文件') : job.incrementalInventory ? '增量清点' : '全量清点'
}

function terminal(job: MaintenanceJob) {
  return job.status === 'COMPLETED' || job.status === 'FAILED' || job.status === 'CANCELLED'
}

export function MaintenancePanel({ overview }: Props) {
  const [sourceID, setSourceID] = useState('')
  const [jobs, setJobs] = useState<MaintenanceJob[]>([])
  const [selectedJobID, setSelectedJobID] = useState('')
  const [candidates, setCandidates] = useState<GCCandidatesPage | null>(null)
  const [repairIssues, setRepairIssues] = useState<RepairIssuesPage | null>(null)
  const [exports, setExports] = useState<ExportJob[]>([])
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const [filter, setFilter] = useState('all')
  const [lastRead, setLastRead] = useState('')
  const [loadedSource, setLoadedSource] = useState('')
  const [readError, setReadError] = useState('')
  const ready = !!sourceID && loadedSource === sourceID && !readError
  const [exportError, setExportError] = useState('')
  const [reportProgress, setReportProgress] = useState<{jobId: string; done: number; total: number} | null>(null)
  const [reportJob, setReportJob] = useState('')
  const [reportCancelling, setReportCancelling] = useState(false)
  useEffect(() => onEvent<{jobId: string; done: number; total: number}>('repair-report-progress', p => { if (p?.jobId === reportJob) setReportProgress(p) }), [reportJob])

  const source = useMemo(() => overview?.sources.find(x => x.source.id === sourceID), [overview, sourceID])
  const selectedJob = useMemo(() => jobs.find(x => x.id === selectedJobID), [jobs, selectedJobID])
  const activeJob = useMemo(() => jobs.find(x => x.status === 'QUEUED' || x.status === 'RUNNING'), [jobs])
  const pausedJob = useMemo(() => jobs.find(x => (x.kind === 'INVENTORY' || x.kind === 'REPAIR') && x.status === 'PAUSED'), [jobs])

  useEffect(() => {
    const sources = overview?.sources ?? []
    if (!sources.length) {
      setSourceID(''); setJobs([]); setSelectedJobID(''); setCandidates(null)
      return
    }
    if (!sources.some(x => x.source.id === sourceID)) setSourceID(sources[0].source.id)
  }, [overview, sourceID])

  useEffect(() => {
    if (!overview || !sourceID || busy) return
    let disposed = false, pending = false
    async function refresh() {
      if (pending) return
      pending = true
      try {
        const value = await app().ListMaintenance(sourceID, 50)
        if (!disposed) { setJobs(value ?? []); setLoadedSource(sourceID); setLastRead(new Date().toLocaleTimeString('zh-CN')); setReadError('') }
      } catch (e) {
        if (!disposed) { setLoadedSource(''); setReadError(errorText(e)) }
      } finally { pending = false }
    }
    void refresh()
    const timer = window.setInterval(refresh, 3000)
    return () => { disposed = true; window.clearInterval(timer) }
  }, [overview, sourceID, busy])

  useEffect(() => {
    setSelectedJobID(''); setCandidates(null); setRepairIssues(null); setMessage(''); setLoadedSource(''); setLastRead(''); setReadError(''); setFilter('all')
  }, [sourceID])

  useEffect(() => {
    if (!overview) { setExports([]); return }
    let disposed = false, pending = false
    async function refresh() {
      if (pending) return
      pending = true
      try {
        const jobs = await app().ListExportJobs()
        if (!disposed) { setExports(jobs ?? []); setExportError('') }
      } catch (e) { if (!disposed) setExportError(errorText(e)) }
      finally { pending = false }
    }
    void refresh()
    const timer = window.setInterval(refresh, 3000)
    return () => { disposed = true; window.clearInterval(timer) }
  }, [overview])

  async function downloadExport(job: ExportJob) {
    setBusy(true); setMessage('')
    try {
      const destination = await app().PickStateDestination()
      if (!destination) return
      await app().DownloadExportJob(job.id, destination)
      setMessage(`状态文件已保存：${destination}`)
    } catch (e) {
      setMessage(errorText(e) + '。如果下载中断，请重新选择这次导出任务，并保存到原来的位置。')
    } finally { setBusy(false) }
  }

  async function reclaimHistory() {
    if (!sourceID || !window.confirm('删除已不再使用或已放弃的历史账本记录？当前文件账本和软件包不会受到影响。删除后，数据库会重复利用腾出的空间，但文件不一定变小。')) return
    setBusy(true); setMessage('')
    let entries = 0, generations = 0
    try {
      for (;;) {
        const result = await app().ReclaimCatalogHistory(sourceID)
        entries += result.removedEntries
        generations += result.removedGenerations
        setMessage(`已回收 ${formatCount(entries)} 条历史记录，${formatCount(generations)} 个历史版本。${result.more ? '仍在处理。' : '已完成。'}`)
        if (!result.more) break
        await new Promise(resolve => window.setTimeout(resolve, 100))
      }
    } catch (e) { setMessage(`已回收 ${formatCount(entries)} 条记录；` + errorText(e)) }
    finally { setBusy(false) }
  }

  async function refreshJobs() {
    if (!sourceID) return
    try { setJobs(await app().ListMaintenance(sourceID, 50) ?? []); setLoadedSource(sourceID); setLastRead(new Date().toLocaleTimeString('zh-CN')); setReadError('') }
    catch (e) { setLoadedSource(''); setReadError(errorText(e)); setMessage(errorText(e)) }
  }

  async function startInventory(incremental = false) {
    if (!sourceID) return
    setBusy(true); setMessage('')
    try {
      const job = await (incremental ? app().StartIncrementalInventory(sourceID) : app().StartInventory(sourceID))
      setSelectedJobID(job.id); setCandidates(null); setMessage(`已开始清点文件。任务编号：${shortID(job.id)}`)
      await refreshJobs()
    } catch (e) { setMessage(errorText(e)) }
    finally { setBusy(false) }
  }

  async function startRepair() {
    if (!sourceID) return
    setBusy(true); setMessage('')
    try {
      const job = await app().StartRepair(sourceID)
      setSelectedJobID(job.id); setCandidates(null); setRepairIssues(null)
      await refreshJobs()
    } catch (e) { setMessage(errorText(e)) }
    finally { setBusy(false) }
  }

  async function startIntegrity() {
    if (!sourceID) return
    const raw = window.prompt('完整校验需要读取账本中记录的所有文件，可能耗时较长。请设置每秒最多读取的数据量（1–1024 MiB/s）。', '64')
    if (raw === null) return
    const speed = Number(raw)
    if (!Number.isInteger(speed) || speed < 1 || speed > 1024) { setMessage('请输入 1 至 1024 的整数。'); return }
    setBusy(true); setMessage('')
    try {
      const job = await app().StartIntegrity(sourceID, speed)
      setSelectedJobID(job.id); setCandidates(null); setRepairIssues(null)
      await refreshJobs()
    } catch (e) { setMessage(errorText(e)) }
    finally { setBusy(false) }
  }

  async function showRepair(job: MaintenanceJob, append = false) {
    setBusy(true); setMessage('')
    try {
      const page = await app().RepairIssues(job.id, append ? repairIssues?.next ?? '' : '', 100)
      setSelectedJobID(job.id); setCandidates(null)
      setRepairIssues(append && repairIssues ? {...page, items: [...repairIssues.items, ...page.items]} : page)
    } catch (e) { setMessage(errorText(e)) }
    finally { setBusy(false) }
  }

  async function approveRepair(job: MaintenanceJob, approve: boolean) {
    const message = approve
      ? '授权此报告中经过 SHA256 校验的损坏 PyPI 软件包进行修复。导入时必须匹配原账本的大小及 SHA256，并再次验证旧文件与报告一致；原损坏文件会保留在隔离目录。确认授权？'
      : '不再允许根据这份报告替换损坏的文件？已经替换的文件不会恢复成原样。'
    if (!window.confirm(message)) return
    setBusy(true); setMessage('')
    try {
      await app().ApproveRepairReplacement(job.id, approve)
      await refreshJobs()
      setMessage(approve ? '已允许修复这些损坏的文件。请导出修复状态文件，再到联网电脑下载并带回内网导入。' : '已撤销授权。')
    } catch (e) { setMessage(errorText(e)) }
    finally { setBusy(false) }
  }

  async function exportRepair(job: MaintenanceJob) {
    setBusy(true); setMessage('')
    try {
      const destination = await app().PickStateDestination()
      if (!destination) return
      const result = await app().ExportRepairCapsule(job.sourceId, job.id, destination)
      setMessage(`修复状态文件已保存：${result.path}。请使用新版联网客户端下载并导入；只有已授权且再次验证的损坏软件包才允许替换。`)
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

  async function resumeJob() {
    if (!pausedJob) return
    setBusy(true); setMessage('')
    try {
      await app().ResumeMaintenance(pausedJob.id)
      setMessage('正在恢复任务。上次保存的进度会保留，未保存的部分可能需要重新检查。')
      await refreshJobs()
    } catch (e) { setMessage(errorText(e)) }
    finally { setBusy(false) }
  }

  async function pauseJob() {
    if (!activeJob || (activeJob.kind !== 'INVENTORY' && activeJob.kind !== 'REPAIR')) return
    setBusy(true); setMessage('')
    try {
      await app().PauseMaintenance(activeJob.id)
      setMessage('正在暂停任务，服务器会保留最近保存的进度。')
      await refreshJobs()
    } catch (e) { setMessage(errorText(e)) }
    finally { setBusy(false) }
  }

  async function cancelJob() {
    const target = activeJob ?? pausedJob
    if (!target) return
    if (target.status === 'PAUSED' && !window.confirm('放弃这次任务？已保存的检查进度将被删除，当前文件账本不会改变。')) return
    setBusy(true); setMessage('')
    try {
      await app().CancelMaintenance(target.id)
      setMessage(`已请求停止任务：${shortID(target.id)}`)
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
      setRepairIssues(null)
      if (append && candidates && selectedJobID === job.id) {
        setCandidates({ ...page, items: [...candidates.items, ...page.items] })
      } else {
        setCandidates(page)
      }
    } catch (e) { setMessage(errorText(e)) }
    finally { setBusy(false) }
  }

  async function exportCompleteReport(jobID: string) {
    setBusy(true); setReportJob(jobID); setReportProgress(null); setReportCancelling(false); setMessage('正在准备导出完整异常报告……')
    try { const result = await app().ExportCompleteRepairReport(jobID); setMessage(result.path ? `已保存完整报告，共 ${formatCount(result.count)} 条异常：${result.path}` : '已取消保存报告。') }
    catch (e) { setMessage('完整报告导出失败：' + errorText(e)) }
    finally { setBusy(false); setReportJob(''); setReportProgress(null); setReportCancelling(false) }
  }
  async function saveReport(kind: 'maintenance' | 'repair') {
    setBusy(true); setMessage('')
    try {
      const data = kind === 'maintenance' ? { kind, exportedAt: new Date().toISOString(), sourceId: sourceID, sourceName: source?.source.name, scope: 'recent-filtered', filter, lastRead, readError, items: visibleJobs } : { kind, exportedAt: new Date().toISOString(), sourceId: sourceID, sourceName: source?.source.name, jobId: selectedJobID, scope: 'loaded-items-only', hasMore: !!repairIssues?.next, items: repairIssues?.items ?? [] }
      const destination = await app().SaveDiagnosticReport(JSON.stringify(data, null, 2))
      if (destination) setMessage(`诊断报告已保存：${destination}。报告可能包含内网路径和错误详情，请妥善保管。`)
    } catch (e) { setMessage('无法保存诊断报告。如果同名文件已经存在，请换一个文件名。' + errorText(e)) }
    finally { setBusy(false) }
  }
  const current = activeJob ?? pausedJob
  const visibleJobs = jobs.filter(j => filter === 'all' || (filter === 'active' && !terminal(j)) || (filter === 'failed' && j.status === 'FAILED') || (filter === 'completed' && j.status === 'COMPLETED'))
  return <>
    {message && <div className="notice" role="status" aria-live="polite">{message}</div>}{reportJob && <div className="card current-task"><h3>正在导出完整异常报告</h3><p>{reportProgress ? `已写入 ${formatCount(reportProgress.done)} / ${formatCount(reportProgress.total)} 条异常记录` : '等待选择文件或读取第一批记录……'}</p>{reportProgress && reportProgress.total > 0 && <progress className="maintenance-progress" max={reportProgress.total} value={reportProgress.done} aria-label="完整异常报告导出进度"/>}{reportProgress && <button disabled={reportCancelling} onClick={async () => { setReportCancelling(true); try { await app().CancelRepairReport(reportJob); setMessage('已请求取消报告导出。') } catch (e) { setReportCancelling(false); setMessage(errorText(e)) } }}>{reportCancelling ? '正在取消…' : '取消导出'}</button>}</div>}
    <div className="card">
      <div className="card-title"><span>维护工具</span><span>检查文件和清理服务器空间</span></div>
      <div className="form-grid">
        <label>软件源<select value={sourceID} onChange={e => { setJobs([]); setSourceID(e.target.value) }} disabled={!overview || busy}>
          {(overview?.sources ?? []).map(x => <option key={x.source.id} value={x.source.id}>{x.source.name} · {x.source.type}/{x.source.provider}</option>)}
        </select></label>
        <div className="span-2"><p>如果服务器里的文件数量不对，可以先重新清点。PyPI 的大型仓库每检查一批项目会保存进度，暂停或 Agent 重启后可继续；整个任务完成前，不会替换原来的文件账本。</p>{source?.source.type === 'apt' && <small>APT 的共享包目录默认不会被清理。只有明确设为该软件源独占时，才允许删除其中的废弃文件。</small>}</div>
        <div className="button-row align-end maintenance-actions">{!ready && sourceID && <p className="small">暂时没有最新的维护记录。可以对列表中的任务尝试暂停、继续或取消。</p>}
          <button disabled={!ready || !!activeJob || !!pausedJob || !!source?.state.activeEpochId || busy} onClick={() => startInventory(false)}>全量清点</button>
          {source?.source.type === 'pypi' && <button disabled={!ready || !!activeJob || !!pausedJob || !!source?.state.activeEpochId || busy} onClick={() => startInventory(true)}>增量清点</button>}
          <button disabled={!ready || !!activeJob || !!pausedJob || !!source?.state.activeEpochId || busy} onClick={() => startGC(false)}>查看可清理文件</button>
          {source?.source.type === 'pypi' && <button disabled={!ready || !!activeJob || !!pausedJob || !!source?.state.activeEpochId || busy} onClick={startRepair}>检查缺失文件</button>}
          {source?.source.type === 'pypi' && <button disabled={!ready || !!activeJob || !!pausedJob || !!source?.state.activeEpochId || busy} onClick={startIntegrity}>SHA256 完整校验</button>}
          {(activeJob?.kind === 'INVENTORY' || activeJob?.kind === 'REPAIR') && source?.source.type === 'pypi' && <button disabled={busy} onClick={pauseJob}>{activeJob.kind === 'REPAIR' ? '暂停校验' : '暂停清点'}</button>}
          {pausedJob && <button className="primary" disabled={busy} onClick={resumeJob}>{pausedJob.kind === 'REPAIR' ? '继续校验' : '继续清点'}</button>}
          {(activeJob || pausedJob) && <button className="danger-ghost" disabled={busy} onClick={cancelJob}>{pausedJob ? (pausedJob.kind === 'REPAIR' ? '放弃校验' : '放弃清点') : '取消任务'}</button>}
          <button disabled={!ready || !!activeJob || !!pausedJob || !!source?.state.activeEpochId || busy} onClick={reclaimHistory}>清理历史账本</button>
          <button disabled={!sourceID || busy} onClick={async () => { setMessage(''); setBusy(true); try { await refreshJobs() } finally { setBusy(false) } }}>刷新</button>
        </div>
      </div>
    </div>

    {current && <div className="card current-task"><div className="card-title"><span>当前维护任务</span><span className={statusClass(current.status)}>{statusLabel(current.status)}</span></div><h3>{taskName(current)}</h3><p>任务编号：<code>{current.id}</code></p><div className="batch-overview"><div><span>已检查文件</span><strong>{formatCount(current.scannedObjects)}</strong></div><div><span>已检查的数据量</span><strong>{formatBytes(current.scannedBytes)}</strong></div><div><span>{current.kind === 'REPAIR' ? '异常文件' : current.kind === 'GC' ? '可清理文件' : '已完成项目'}</span><strong>{formatCount(current.kind === 'INVENTORY' ? current.completedProjects : current.candidateObjects)}</strong></div></div>{current.kind === 'INVENTORY' && current.totalProjects > 0 && <><progress className="maintenance-progress" aria-label="当前项目清点进度" max={current.totalProjects} value={Math.min(current.totalProjects, Math.max(0, current.completedProjects))}/><p>{formatCount(current.completedProjects)} / {formatCount(current.totalProjects)} 个项目{current.lastProject ? `；最近完成：${current.lastProject}` : ''}</p></>}<p>开始时间：{formatDate(current.startedAt)}</p>{current.status === 'PAUSED' && <p>任务已暂停，可从服务器保存的检查点继续。</p>}{current.errorText && <p className="error-detail">{current.errorText}</p>}</div>}
    <div className="card table-card">
      <div className="card-title"><span>维护记录</span><span>{lastRead ? `最近查询 ${lastRead}` : '尚未读取'}</span></div>
      <div className="table-toolbar"><label>筛选任务<select value={filter} onChange={e => setFilter(e.target.value)}><option value="all">全部任务</option><option value="active">未结束</option><option value="failed">失败</option><option value="completed">已完成</option></select></label><span>显示 {visibleJobs.length} / {jobs.length} 条近期记录</span><button disabled={busy || !visibleJobs.length} onClick={() => saveReport('maintenance')}>导出当前筛选结果</button>{readError && <span className="error-detail">读取失败：{readError}（列表可能不是最新状态）</span>}</div>
      <div className="table-wrap"><table><thead><tr><th>任务</th><th>类型</th><th>状态</th><th>已检查</th><th>异常或可清理文件</th><th>实际删除</th><th>时间</th><th></th></tr></thead>
        <tbody>{visibleJobs.length ? visibleJobs.map(job => <tr key={job.id}>
          <td><code>{shortID(job.id)}</code>{job.errorText && <small className="error-detail">{job.errorText}</small>}</td>
          <td>{job.kind === 'GC' ? '清理文件' : job.kind === 'REPAIR' ? (job.hashMiBPerSecond ? `完整性校验（${job.hashMiBPerSecond} MiB/s）` : '检查缺失文件') : job.incrementalInventory ? '增量清点' : '全量清点'}{job.kind === 'GC' && <small>{job.execute ? '执行删除' : '仅预览'}</small>}</td>
          <td><span className={statusClass(job.status)}>{statusLabel(job.status)}</span></td>
          <td>{formatCount(job.scannedObjects)}<small>{formatBytes(job.scannedBytes)}</small>{job.kind === 'INVENTORY' && job.totalProjects > 0 && <><progress className="maintenance-progress" aria-label="项目清点进度" max={job.totalProjects} value={Math.min(job.totalProjects, Math.max(0, job.completedProjects))} /><small>已检查项目 {formatCount(job.completedProjects)}/{formatCount(job.totalProjects)}{job.lastProject ? ` · 最近完成 ${job.lastProject}` : ''}</small></>}</td>
          <td>{job.kind === 'GC' || job.kind === 'REPAIR' ? <>{formatCount(job.candidateObjects)}<small>{formatBytes(job.candidateBytes)}</small></> : '—'}</td>
          <td>{job.kind === 'GC' && job.execute ? <>{formatCount(job.affectedObjects)}<small>{formatBytes(job.affectedBytes)}</small></> : '—'}</td>
          <td>{formatDate(job.createdAt)}<small>{job.completedAt ? `完成 ${formatDate(job.completedAt)}` : job.startedAt ? `开始 ${formatDate(job.startedAt)}` : ''}</small></td>
          <td><div className="button-row">{job.kind === 'REPAIR' && terminal(job) && <button disabled={busy} onClick={() => showRepair(job)}>查看异常</button>}{job.kind === 'REPAIR' && job.status === 'COMPLETED' && job.candidateObjects > 0 && <button disabled={!ready || busy || !!activeJob || !!pausedJob || !!source?.state.activeEpochId} onClick={() => exportRepair(job)}>导出修复状态</button>}{job.kind === 'REPAIR' && job.status === 'COMPLETED' && !!job.hashMiBPerSecond && job.candidateObjects > 0 && <button disabled={!ready || busy || !!activeJob || !!pausedJob || !!source?.state.activeEpochId} onClick={() => approveRepair(job, !job.replacementApproved)}>{job.replacementApproved ? '撤销损坏文件修复授权' : '授权损坏文件修复'}</button>}{job.kind === 'GC' && terminal(job) && job.tempCatalogPath && <button disabled={busy} onClick={() => showCandidates(job)}>查看列表</button>}{job.kind === 'GC' && job.status === 'COMPLETED' && !job.execute && <button className="danger-ghost" disabled={!ready || busy || !!activeJob || !!pausedJob} onClick={() => { setSelectedJobID(job.id); void showCandidates(job).then(() => undefined) }}>选择这次预览结果</button>}</div></td>
        </tr>) : <tr><td colSpan={8} className="empty">{readError ? '未能读取维护记录，请检查连接后刷新。' : '没有符合条件的近期维护记录。'}</td></tr>}</tbody>
      </table></div>
    </div>

    {selectedJob?.kind === 'GC' && selectedJob.status === 'COMPLETED' && !selectedJob.execute && <div className="card">
      <div className="card-title"><span>确认清理</span><span>{shortID(selectedJob.id)}</span></div>
      <div className="button-row"><button className="danger-ghost" disabled={!ready || busy || !!activeJob || !!pausedJob || !!source?.state.activeEpochId} onClick={() => startGC(true)}>重新检查并删除文件</button><p>删除前会再次检查文件是否还被仓库使用，不会直接按旧清单删除。</p></div>
    </div>}

    <div className="card table-card">
      <div className="card-title"><span>状态文件导出任务</span><span>如果下载中断，选择这次任务和原来的保存位置即可继续。</span></div>{exportError && <p className="error-detail export-empty">暂时无法获取导出任务：{exportError}。如果服务器仍在使用旧版 Agent，可能不支持此功能。</p>}{!exports.length && !exportError && <p className="empty">暂无状态文件导出任务。</p>}
      <div className="table-wrap"><table><thead><tr><th>任务</th><th>状态</th><th>进度</th><th>时间</th><th></th></tr></thead><tbody>
        {exports.map(job => <tr key={job.id}><td><code>{shortID(job.id)}</code>{job.repairJobId && <small>缺失文件修复</small>}</td><td>{statusLabel(job.status)}</td><td>{job.stage}<small>{job.completedSources}/{job.totalSources} 个软件源</small>{job.errorText && <small>{job.errorText}</small>}</td><td>{formatDate(job.createdAt)}</td><td>{job.status === 'COMPLETED' && <button disabled={busy} onClick={() => downloadExport(job)}>下载或继续下载</button>}</td></tr>)}
      </tbody></table></div>
    </div>
    {repairIssues && <div className="card table-card">
      <div className="card-title"><span>文件异常</span><span>缺失文件可离线补回；损坏软件包须经过 SHA256 校验并明确授权后才能替换</span></div><div className="table-toolbar"><span>已显示 {formatCount(repairIssues.items.length)} 条异常{repairIssues.next ? '，还有更多记录' : ''}</span><button disabled={busy || !repairIssues.items.length} onClick={() => saveReport('repair')}>导出已显示的异常记录</button>{selectedJob?.status === 'COMPLETED' && <button className="primary" disabled={busy} onClick={() => exportCompleteReport(selectedJob.id)}>导出完整异常报告</button>}</div>
      <div className="table-wrap"><table><thead><tr><th>文件路径</th><th>类型</th><th>账本记录的大小</th><th>实际大小</th></tr></thead><tbody>
        {repairIssues.items.map(item => <tr key={item.logicalPath}><td><code>{item.logicalPath}</code>{item.actualSha256 && <small>预期：{item.sha256}<br/>实际：{item.actualSha256}</small>}</td><td>{item.kind === 'MISSING' ? '缺失' : item.kind === 'HASH_MISMATCH' ? 'SHA256 不符' : '大小不符'}</td><td>{formatBytes(item.size)}</td><td>{item.kind === 'MISSING' ? '—' : formatBytes(item.actualSize)}</td></tr>)}
      </tbody></table></div>
      {repairIssues.next && selectedJob && <button disabled={busy} onClick={() => showRepair(selectedJob, true)}>加载更多</button>}
    </div>}
    {candidates && <div className="card table-card">
      <div className="card-title"><span>可能不再需要的文件</span><span>{formatCount(candidates.stats.objects)} · {formatBytes(candidates.stats.bytes)}</span></div>
      <div className="table-wrap"><table><thead><tr><th>文件路径</th><th>大小</th><th>扫描时记录的修改时间</th></tr></thead><tbody>
        {candidates.items.length ? candidates.items.map(item => <tr key={item.logicalPath}><td><code>{item.logicalPath}</code></td><td>{formatBytes(item.size)}</td><td>{new Date(item.modTimeUnixNano / 1_000_000).toLocaleString()}</td></tr>) : <tr><td colSpan={3} className="empty">没有发现需要清理的文件。</td></tr>}
      </tbody></table></div>
      {candidates.next && <div className="button-row" style={{ padding: 14 }}><button disabled={busy || !selectedJob} onClick={() => selectedJob && showCandidates(selectedJob, true)}>加载更多</button></div>}
    </div>}
  </>
}
