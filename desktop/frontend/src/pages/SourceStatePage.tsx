import { useEffect, useMemo, useState } from 'react'
import { app } from '../backend'
import { MaintenancePanel } from '../components/MaintenancePanel'
import { SourceEditor } from '../components/SourceEditor'
import { errorText, formatBytes, formatCount, formatDate, shortID, statusLabel } from '../format'
import type { AgentOverview, Epoch, Source, SourceInput } from '../types'

type Props = {
  overview: AgentOverview | null
  onOverview: (value: AgentOverview | null) => void
  onCapsuleChanged: () => Promise<void>
}

export function SourceStatePage({ overview, onOverview, onCapsuleChanged }: Props) {
  const [baseURL, setBaseURL] = useState('')
  const [token, setToken] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [destination, setDestination] = useState('')
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const [editor, setEditor] = useState<Source | 'create' | null>(null)
  const [epochSourceID, setEpochSourceID] = useState('')
  const [epochs, setEpochs] = useState<Epoch[]>([])
  const selectedIDs = useMemo(() => [...selected], [selected])

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

  async function connect() {
    if (!baseURL.trim()) { setMessage('请填写内网服务器地址，例如 http://192.168.21.4:8787'); return }
    setBusy(true); setMessage('')
    try {
      const value = await app().ConnectAgent(baseURL, token)
      onOverview(value)
      setSelected(new Set(value.sources.filter(x => x.source.enabled).map(x => x.source.id)))
    } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }
  async function refresh() {
    setBusy(true); setMessage('')
    try { onOverview(await app().RefreshAgent()) } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }
  async function disconnect() {
    await app().DisconnectAgent(); onOverview(null); setSelected(new Set()); setEditor(null)
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

    <p>请填写 Linux 服务器的实际地址和端口，例如 <code>http://192.168.21.4:8787</code>。连接令牌由服务器管理员提供；不清楚地址时，可先在 Linux 上运行配置脚本。</p>
    <div className="card form-grid agent-form">
      <label>内网服务器地址<input value={baseURL} onChange={e => setBaseURL(e.target.value)} disabled={!!overview} placeholder="例如 http://192.168.21.4:8787" /></label>
      <label>连接令牌<input type="password" value={token} onChange={e => setToken(e.target.value)} disabled={!!overview} placeholder="服务器管理员提供的连接令牌" /></label>
      <div className="button-row align-end">
        {!overview ? <button className="primary" disabled={busy} onClick={connect}>连接服务器</button> : <>
          <button disabled={busy} onClick={refresh}>刷新</button><button className="danger-ghost" onClick={disconnect}>断开连接</button>
        </>}
      </div>
    </div>

    <div className="card table-card">
      <div className="card-title">
        <span>选择要更新的软件源</span>
        <div className="button-row">{overview ? <><span>{overview.sources.length} 个源</span><button disabled={busy} onClick={() => setEditor('create')}>新建源</button></> : <span>连接后可以选择</span>}</div>
      </div>
      <div className="table-wrap"><table><thead><tr><th></th><th>软件源</th><th>类型</th><th>同步记录</th><th>现有文件</th><th>服务器空间</th><th>同步任务</th><th>更新时间</th><th></th></tr></thead>
        <tbody>{overview?.sources.length ? overview.sources.map(row => {
          const used = Math.max(0, row.capacity.totalBytes - row.capacity.freeBytes)
          const pct = row.capacity.totalBytes ? Math.min(100, used / row.capacity.totalBytes * 100) : 0
          return <tr key={row.source.id}>
            <td><input type="checkbox" checked={selected.has(row.source.id)} onChange={e => setSelected(prev => { const n = new Set(prev); e.target.checked ? n.add(row.source.id) : n.delete(row.source.id); return n })} /></td>
            <td><strong>{row.source.name}</strong><small>{shortID(row.source.id)}</small></td>
            <td><span className="pill">{row.source.type}</span><small>{row.source.provider}</small></td>
            <td>{row.state.liveCursor.value ? '有' : '暂无'}<small className="mono clip">{row.state.liveCursor.kind}</small></td>
            <td>{formatCount(row.state.liveObjects)} 个<small>{formatBytes(row.state.liveBytes)}</small></td>
            <td>剩余 {formatBytes(row.capacity.freeBytes)}<small>总计 {formatBytes(row.capacity.totalBytes)} · 已用 {pct.toFixed(1)}%</small></td>
            <td>{row.state.activeEpochId ? '有任务进行中' : '无'}</td>
            <td>{formatDate(row.state.updatedAt)}</td>
            <td><button disabled={busy} onClick={() => setEditor(row.source)}>编辑</button></td>
          </tr>
        }) : <tr><td colSpan={9} className="empty">连接服务器后，就能在这里选择需要更新的软件源。</td></tr>}</tbody>
      </table></div>
    </div>

    {overview && editor && <SourceEditor key={editor === 'create' ? 'new' : editor.id} source={editor === 'create' ? null : editor} busy={busy} onCancel={() => setEditor(null)} onSubmit={saveSource} />}

    {overview && <details className="secondary-section"><summary>查看同步任务和维护工具（需要时再打开）</summary><div className="secondary-section-body"><div className="card table-card">
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
          <td>{formatBytes(e.totalBytes)}<small>{formatCount(e.totalObjects)} objects</small></td>
          <td>{e.totalBatches}</td>
          <td>{formatDate(e.createdAt)}</td>
          <td>{e.status !== 'COMPLETE' && e.status !== 'CANCELLED' ? <button className="danger-ghost" disabled={busy} onClick={() => cancelEpoch(e)}>取消</button> : null}</td>
        </tr>) : <tr><td colSpan={7} className="empty">这个软件源还没有同步任务。</td></tr>}</tbody>
      </table></div>
    </div>
    <MaintenancePanel overview={overview} />
    </div></details>}

    <div className="card export-row">
      <div><h3>把状态文件保存到移动硬盘</h3><p>这个文件只记录服务器目前有哪些软件，不包含安装包。带到联网电脑后，用它检查和下载更新。</p></div>
      <div className="path-picker"><input value={destination} onChange={e => setDestination(e.target.value)} placeholder="选择移动硬盘上的保存位置（.mstate）" /><button onClick={chooseDestination}>浏览</button><button className="primary" disabled={!overview || busy || !selectedIDs.length} onClick={exportCapsule}>保存状态文件 {selectedIDs.length ? `(${selectedIDs.length})` : ''}</button></div>
    </div>
    {message && <div className="notice">{message}</div>}
  </section>
}
