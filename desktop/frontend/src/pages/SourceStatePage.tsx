import { useMemo, useState } from 'react'
import { app } from '../backend'
import { errorText, formatBytes, formatCount, formatDate, shortID } from '../format'
import type { AgentOverview } from '../types'

type Props = {
  overview: AgentOverview | null
  onOverview: (value: AgentOverview | null) => void
  onCapsuleChanged: () => Promise<void>
}

export function SourceStatePage({ overview, onOverview, onCapsuleChanged }: Props) {
  const [baseURL, setBaseURL] = useState('http://127.0.0.1:8787')
  const [token, setToken] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [destination, setDestination] = useState('')
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const selectedIDs = useMemo(() => [...selected], [selected])

  async function connect() {
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
    await app().DisconnectAgent(); onOverview(null); setSelected(new Set())
  }
  async function chooseDestination() {
    const value = await app().PickStateDestination()
    if (value) setDestination(value)
  }
  async function exportCapsule() {
    if (!selectedIDs.length) { setMessage('至少选择一个源。'); return }
    let target = destination
    if (!target) target = await app().PickStateDestination()
    if (!target) return
    setDestination(target); setBusy(true); setMessage('')
    try {
      const result = await app().ExportStateCapsule(selectedIDs, target)
      setMessage(`State Capsule 已保存：${result.path}`)
      await onCapsuleChanged()
    } catch (e) { setMessage(errorText(e)) } finally { setBusy(false) }
  }

  return <section className="page-stack">
    <div className="page-header">
      <div><h2>源状态</h2><p>连接内网 Mirror Agent，查看实时游标、容量并导出 State Capsule。</p></div>
      <span className={`connection ${overview?.connected ? 'ok' : ''}`}>{overview?.connected ? '已连接' : '未连接'}</span>
    </div>

    <div className="card form-grid agent-form">
      <label>Agent URL<input value={baseURL} onChange={e => setBaseURL(e.target.value)} disabled={!!overview} /></label>
      <label>Bearer Token<input type="password" value={token} onChange={e => setToken(e.target.value)} disabled={!!overview} placeholder="AIRGAP_MIRROR_TOKEN" /></label>
      <div className="button-row align-end">
        {!overview ? <button className="primary" disabled={busy} onClick={connect}>连接</button> : <>
          <button disabled={busy} onClick={refresh}>刷新</button><button className="danger-ghost" onClick={disconnect}>断开</button>
        </>}
      </div>
    </div>

    <div className="card table-card">
      <div className="card-title"><span>Mirror Sources</span><span>{overview ? `${overview.sources.length} 个源` : '等待连接'}</span></div>
      <div className="table-wrap"><table><thead><tr><th></th><th>源</th><th>类型 / Provider</th><th>Live Cursor</th><th>对象 / 数据量</th><th>文件系统容量</th><th>Active Epoch</th><th>更新时间</th></tr></thead>
        <tbody>{overview?.sources.length ? overview.sources.map(row => {
          const used = Math.max(0, row.capacity.totalBytes - row.capacity.freeBytes)
          const pct = row.capacity.totalBytes ? Math.min(100, used / row.capacity.totalBytes * 100) : 0
          return <tr key={row.source.id}>
            <td><input type="checkbox" checked={selected.has(row.source.id)} onChange={e => setSelected(prev => { const n = new Set(prev); e.target.checked ? n.add(row.source.id) : n.delete(row.source.id); return n })} /></td>
            <td><strong>{row.source.name}</strong><small>{shortID(row.source.id)}</small></td>
            <td><span className="pill">{row.source.type}</span><small>{row.source.provider}</small></td>
            <td><code>{row.state.liveCursor.kind}</code><small className="mono clip">{row.state.liveCursor.value || '∅'}</small></td>
            <td>{formatCount(row.state.liveObjects)}<small>{formatBytes(row.state.liveBytes)}</small></td>
            <td>{formatBytes(row.capacity.freeBytes)} free<small>{formatBytes(row.capacity.totalBytes)} total · {pct.toFixed(1)}% used</small></td>
            <td>{row.state.activeEpochId ? <code>{shortID(row.state.activeEpochId)}</code> : '—'}</td>
            <td>{formatDate(row.state.updatedAt)}</td>
          </tr>
        }) : <tr><td colSpan={8} className="empty">连接 Agent 后显示源状态。</td></tr>}</tbody>
      </table></div>
    </div>

    <div className="card export-row">
      <div><h3>导出 State Capsule</h3><p>只包含源状态与 Catalog 快照，不包含制品本体。复制到互联网电脑后用于增量分析。</p></div>
      <div className="path-picker"><input value={destination} onChange={e => setDestination(e.target.value)} placeholder="选择 .mstate 保存位置" /><button onClick={chooseDestination}>浏览</button><button className="primary" disabled={!overview || busy || !selectedIDs.length} onClick={exportCapsule}>导出 {selectedIDs.length ? `(${selectedIDs.length})` : ''}</button></div>
    </div>
    {message && <div className="notice">{message}</div>}
  </section>
}
