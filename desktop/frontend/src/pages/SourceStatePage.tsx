import { useEffect, useMemo, useState } from 'react'
import { app } from '../backend'
import { SourceEditor } from '../components/SourceEditor'
import { errorText, formatBytes, formatCount, formatDate, shortID, statusLabel } from '../format'
import type { ConnectionCheck, AgentOverview, Epoch, Source, SourceInput } from '../types'

type Props = {
  overview: AgentOverview | null
  onOverview: (value: AgentOverview | null) => void
  onCapsuleChanged: () => Promise<void>
  onMaintenance: () => void
}

export function SourceStatePage({ overview, onOverview, onCapsuleChanged, onMaintenance }: Props) {
  const [baseURL, setBaseURL] = useState('')
  const [token, setToken] = useState('')
  const [checks, setChecks] = useState<ConnectionCheck[]>([])
  const [diagnosing, setDiagnosing] = useState(false)
  const [search, setSearch] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [destination, setDestination] = useState('')
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const [editor, setEditor] = useState<Source | 'create' | null>(null)
  const [epochSourceID, setEpochSourceID] = useState('')
  const [epochs, setEpochs] = useState<Epoch[]>([])
  const selectedIDs = useMemo(() => (overview?.sources ?? []).filter(row => selected.has(row.source.id)).map(row => row.source.id), [selected, overview])
  const visibleSources = useMemo(() => { const q = search.trim().toLowerCase(); return (overview?.sources ?? []).filter(row => [row.source.name, row.source.type, row.source.provider, row.source.rootPath].join(' ').toLowerCase().includes(q)) }, [overview, search])

  const epochSource = useMemo(() => overview?.sources.find(x => x.source.id === epochSourceID), [overview, epochSourceID])

  useEffect(() => {
    const sources = overview?.sources ?? []
    if (!sources.length) {
      setEpochSourceID('')
      return
    }
    if (!sources.some(x => x.source.id === epochSourceID)) {
      const stuck = sources.find(x => x.state.activeEpochId)
      setEpochSourceID((stuck ?? sources[0]).source.id)
    }
  }, [overview, epochSourceID])

  useEffect(() => {
    if (!overview || !epochSourceID) {
      setEpochs([])
      return
    }
    let disposed = false
    app().ListAgentEpochs(epochSourceID).then(items => {
      if (!disposed) setEpochs(items ?? [])
    }).catch(e => {
      if (!disposed) setMessage(errorText(e))
    })
    return () => { disposed = true }
  }, [overview, epochSourceID])

  async function diagnose() {
    setDiagnosing(true); setChecks([]); setMessage('')
    try { setChecks(await app().DiagnoseAgent(baseURL, token)) }
    catch { setMessage('无法执行连接诊断，请检查客户端版本。') }
    finally { setDiagnosing(false) }
  }
  async function connect() {
    if (!baseURL.trim()) { setMessage('请填写内网服务器地址，例如 http://192.168.21.4:8787'); return }
    setBusy(true); setMessage(''); setChecks([])
    try {
      const value = await app().ConnectAgent(baseURL, token)
      onOverview(value)
      setToken('')
      setSelected(new Set(value.sources.filter(x => x.source.enabled).map(x => x.source.id)))
    } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }
  async function refresh() {
    setBusy(true); setMessage('')
    try { onOverview(await app().RefreshAgent()) } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }
  async function disconnect() {
    try { await app().DisconnectAgent(); onOverview(null); setSelected(new Set()); setEditor(null); setToken(''); setMessage('已断开服务器连接。') } catch (e) { setMessage(errorText(e)) }
  }
  async function saveSource(input: SourceInput) {
    setBusy(true); setMessage('')
    try {
      const value = input.id ? await app().UpdateSource(input) : await app().CreateSource(input)
      onOverview(value)
      setSelected(prev => {
        const alive = new Set(value.sources.map(row => row.source.id))
        return new Set([...prev].filter(id => alive.has(id)))
      })
      setEditor(null)
      setMessage(input.id ? `“${input.name}”的设置已保存。` : `已添加“${input.name}”。现在可以导出它的状态文件。`)
    } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }
  async function chooseDestination() {
    const value = await app().PickStateDestination()
    if (value) setDestination(value)
  }
  async function cancelEpoch(epoch: Epoch) {
    if (epoch.status === 'COMPLETE' || epoch.status === 'CANCELLED') return
    if (!window.confirm(`确定取消这次同步吗？如果已经有文件写入服务器，请继续完成导入，避免仓库内容不一致。`)) return
    setBusy(true); setMessage('')
    try {
      const value = await app().CancelEpoch(epoch.id)
      onOverview(value)
      setEpochs(await app().ListAgentEpochs(epochSourceID) ?? [])
      setMessage(`已取消这次同步任务（${shortID(epoch.id)}）。`)
    } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }
  async function exportCapsule() {
    if (!selectedIDs.length) { setMessage('请先勾选至少一个要更新的软件源。'); return }
    let target = destination
    if (!target) target = await app().PickStateDestination()
    if (!target) return
    setDestination(target); setBusy(true); setMessage('')
    try {
      const result = await app().ExportStateCapsule(selectedIDs, target)
      setMessage(`状态文件已保存到：${result.path}。接下来把它带到联网电脑，在“联网下载”页面打开。`)
      await onCapsuleChanged()
    } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }

  return <section className="page-stack">
    <div className="page-header">
      <div><h2>先从内网服务器导出状态文件</h2><p>连接服务器，勾选这次想更新的软件源，再把状态文件保存到移动硬盘。联网电脑会用它找出需要下载的更新。</p></div>
      <span className={`connection ${overview?.connected ? 'ok' : ''}`}>{overview?.connected ? '已连接' : '未连接'}</span>
    </div>

    {message && <div className="notice" role="status" aria-live="polite">{message}</div>}
    <p>请使用管理员提供的 Agent 地址和连接令牌。可以直接连接 <code>http://192.168.21.4:8787</code>，也可以使用已配置的 HTTPS 反向代理地址，例如 <code>https://mirror.costar.com/mirror-control</code>。</p>
    <div className="card form-grid agent-form">
      <label>内网服务器地址<input value={baseURL} onChange={e => { setBaseURL(e.target.value); setChecks([]) }} disabled={!!overview || busy || diagnosing} placeholder="例如 http://192.168.21.4:8787" /></label>
      <label>连接令牌<input type="password" value={token} onChange={e => { setToken(e.target.value); setChecks([]) }} onKeyDown={e => { if (e.key === 'Enter' && !busy && !diagnosing && !overview) void connect() }} disabled={!!overview || busy || diagnosing} placeholder="服务器管理员提供的连接令牌" /></label>
      <div className="button-row align-end">
        {!overview ? <button className="primary" disabled={busy || diagnosing} onClick={connect}>连接服务器</button> : <>
          <button disabled={busy} onClick={refresh}>刷新</button><button className="danger-ghost" disabled={busy} onClick={disconnect}>断开连接</button>
        </>}
      </div>
    </div>
    {!overview && <div className="card connection-diagnostics"><div className="card-title"><span>连接诊断</span><button disabled={busy || diagnosing} onClick={diagnose}>{diagnosing ? '正在检查…' : '检查地址和访问权限'}</button></div><p>仅发送读取请求，不建立正式连接、不修改服务器配置。最多检查 15 秒。</p>{checks.map(check => <div className="check-row" key={check.stage}><span className={`status ${check.ok ? 'status-complete' : 'status-failed'}`}>{check.ok ? '通过' : '未通过'}</span><div><strong>{check.stage}</strong><p>{check.message}</p></div><small>{check.elapsedMs} ms（累计）</small></div>)}</div>}


    {overview && <div className="metrics-grid"><div className="metric"><span>软件源总数</span><strong>{formatCount(overview.sources.length)}</strong></div><div className="metric"><span>已启用</span><strong>{formatCount(overview.sources.filter(x => x.source.enabled).length)}</strong></div><div className="metric"><span>正在同步</span><strong>{formatCount(overview.sources.filter(x => x.state.activeEpochId).length)}</strong></div><div className="metric"><span>已选择导出</span><strong>{formatCount(selectedIDs.length)}</strong></div></div>}
    <div className="card table-card">
      <div className="card-title">
        <span>选择要更新的软件源</span>
        <div className="button-row">{overview ? <><span>{overview.sources.length} 个软件源</span><button disabled={busy} onClick={() => setEditor('create')}>添加软件源</button></> : <span>连接后可以选择</span>}</div>
      </div>
      {overview && <div className="table-toolbar"><input type="search" aria-label="搜索软件源" placeholder="按名称、类型或目录搜索" value={search} onChange={e => setSearch(e.target.value)} /><span>显示 {visibleSources.length} / {overview.sources.length} 个软件源</span><div className="button-row"><button disabled={busy || !visibleSources.length} onClick={() => setSelected(prev => new Set([...prev, ...visibleSources.filter(x => x.source.enabled).map(x => x.source.id)]))}>选择当前列表中已启用的软件源</button><button disabled={busy || !selectedIDs.length} onClick={() => setSelected(new Set())}>取消全部选择</button></div></div>}
      <div className="table-wrap"><table><thead><tr><th>选择</th><th>软件源</th><th>类型</th><th>同步记录</th><th>现有文件</th><th>服务器空间</th><th>同步任务</th><th>更新时间</th><th></th></tr></thead>
        <tbody>{visibleSources.length ? visibleSources.map(row => {
          const valid = !row.capacityError && Number.isFinite(row.capacity.totalBytes) && row.capacity.totalBytes > 0 && Number.isFinite(row.capacity.freeBytes) && row.capacity.freeBytes >= 0 && row.capacity.freeBytes <= row.capacity.totalBytes
          const pct = valid ? (row.capacity.totalBytes - row.capacity.freeBytes) / row.capacity.totalBytes * 100 : 0
          return <tr key={row.source.id}>
            <td><input type="checkbox" aria-label={`选择 ${row.source.name}`} disabled={busy} checked={selected.has(row.source.id)} onChange={e => setSelected(prev => { const n = new Set(prev); e.target.checked ? n.add(row.source.id) : n.delete(row.source.id); return n })} /></td>
            <td><strong>{row.source.name}</strong><small title={row.source.rootPath}>{row.source.enabled ? '已启用' : '已停用'} · {shortID(row.source.id)}</small></td>
            <td><span className="pill">{row.source.type}</span><small>{row.source.provider}</small></td>
            <td>{row.state.liveCursor.kind || '暂无同步记录'}<small className="mono clip" title={row.state.liveCursor.value}>{row.state.liveCursor.value || '—'}</small></td>
            <td>{formatCount(row.state.liveObjects)} 个<small>{formatBytes(row.state.liveBytes)}</small></td>
            <td>{valid ? <>剩余 {formatBytes(row.capacity.freeBytes)}<progress className={`disk-meter ${pct >= 95 ? 'critical' : pct >= 85 ? 'warning' : ''}`} max={100} value={pct} aria-label="所在磁盘已用比例"/><small>总计 {formatBytes(row.capacity.totalBytes)} · 已用 {pct.toFixed(1)}%</small>{pct >= 85 && <span className={`status ${pct >= 95 ? 'status-failed' : 'status-paused'}`}>{pct >= 95 ? '空间紧张' : '空间偏低'}</span>}</> : <small>无法读取磁盘容量{row.capacityError && <details><summary>错误详情</summary><span className="error-detail">{row.capacityError}</span></details>}</small>}</td>
            <td>{row.state.activeEpochId ? '有任务进行中' : '无'}</td>
            <td>{formatDate(row.state.updatedAt)}</td>
            <td><button disabled={busy} onClick={() => setEditor(row.source)}>编辑</button></td>
          </tr>
        }) : <tr><td colSpan={9} className="empty">{overview ? (overview.sources.length ? '没有符合搜索条件的软件源。' : '服务器尚未配置软件源，请先添加。') : '连接服务器后，就能在这里选择需要更新的软件源。'}</td></tr>}</tbody>
      </table></div>
    </div>

    {overview && editor && <SourceEditor key={editor === 'create' ? 'new' : editor.id} source={editor === 'create' ? null : editor} busy={busy} onCancel={() => setEditor(null)} onSubmit={saveSource} />}

    {overview && <details className="secondary-section"><summary>查看服务器同步任务记录</summary><div className="secondary-section-body"><div className="card table-card">
      <div className="card-title">
        <span>同步任务记录</span>
        <span>{epochSource?.state.activeEpochId ? `有正在进行的任务` : '目前没有进行中的任务'}</span>
      </div>
      <div className="form-grid">
        <label>软件源<select value={epochSourceID} onChange={e => setEpochSourceID(e.target.value)} disabled={busy}>
          {(overview.sources ?? []).map(x => <option key={x.source.id} value={x.source.id}>{x.source.name}{x.state.activeEpochId ? ' · 正在同步' : ''}</option>)}
        </select></label>
      </div>
      <div className="table-wrap"><table><thead><tr><th>任务编号</th><th>状态</th><th>更新范围</th><th>数据量</th><th>批次</th><th>创建时间</th><th></th></tr></thead>
        <tbody>{epochs.length ? epochs.map(e => <tr key={e.id}>
          <td><code>{shortID(e.id)}</code></td>
          <td><span className={`status status-${e.status.toLowerCase()}`}>{statusLabel(e.status)}</span></td>
          <td><small className="mono clip">{e.baseCursor.value || '∅'} → {e.targetCursor.value || '∅'}</small></td>
          <td>{formatBytes(e.totalBytes)}<small>{formatCount(e.totalObjects)} 个文件</small></td>
          <td>{e.totalBatches}</td>
          <td>{formatDate(e.createdAt)}</td>
          <td>{e.status !== 'COMPLETE' && e.status !== 'CANCELLED' ? <button className="danger-ghost" disabled={busy} onClick={() => cancelEpoch(e)}>取消</button> : null}</td>
        </tr>) : <tr><td colSpan={7} className="empty">这个软件源还没有同步任务。</td></tr>}</tbody>
      </table></div>
    </div>
    </div></details>}

    {overview && <div className="card settings-card"><div><h3>仓库维护</h3><p>如需清点、校验、修复文件或下载后台导出任务，请打开独立维护页面。</p></div><button onClick={onMaintenance}>打开维护工具</button></div>}
    <div className="card export-row">
      <div><h3>把状态文件保存到移动硬盘</h3><p>状态文件包含服务器的仓库记录和软件源配置，不包含软件包。带到联网电脑后，用它检查和下载更新。</p></div>
      <div className="path-picker"><input value={destination} onChange={e => setDestination(e.target.value)} placeholder="选择移动硬盘上的保存位置（.mstate）" /><button onClick={chooseDestination}>浏览</button><button className="primary" disabled={!overview || busy || !selectedIDs.length} onClick={exportCapsule}>保存状态文件 {selectedIDs.length ? `(${selectedIDs.length})` : ''}</button></div>
    </div>
  </section>
}
