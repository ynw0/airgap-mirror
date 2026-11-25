import { useEffect, useState } from 'react'
import { app, onEvent } from './backend'
import { errorText, formatBytes, formatCount, formatDate, shortID } from './format'
import { InternetSyncPage } from './pages/InternetSyncPage'
import { ServerUpdatePage } from './pages/ServerUpdatePage'
import { SourceStatePage } from './pages/SourceStatePage'
import { MaintenancePanel } from './components/MaintenancePanel'
import type { AgentOverview, Bootstrap, StateCapsule } from './types'

type Tab = 'home' | 'sources' | 'sync' | 'server' | 'maintenance' | 'settings'
type Theme = 'system' | 'light' | 'dark'
const tabs: { id: Tab; label: string; detail: string }[] = [
  { id: 'home', label: '概览', detail: '查看更新和仓库状态' },
  { id: 'sources', label: '连接服务器', detail: '导出内网仓库状态' },
  { id: 'sync', label: '联网下载', detail: '检查更新并下载' },
  { id: 'server', label: '离线导入', detail: '检查并导入服务器' },
  { id: 'maintenance', label: '仓库维护', detail: '清点、校验与导出任务' },
  { id: 'settings', label: '应用设置', detail: '外观和工作目录' },
]
function NavIcon({ name }: { name: Tab }) {
  return <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    {name === 'home' && <><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/></>}
    {name === 'sources' && <><rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01M11 7.5h6M11 16.5h6"/></>}
    {name === 'sync' && <><path d="M12 3v13m-5-5 5 5 5-5M4 16v4h16v-4"/></>}
    {name === 'server' && <><path d="M12 17V4m-5 5 5-5 5 5M4 16v4h16v-4"/></>}
    {name === 'maintenance' && <><rect x="4" y="3" width="16" height="18" rx="2"/><path d="m8 9 1 1 2-2m-3 7 1 1 2-2m2-5h4m-4 6h4"/></>}
    {name === 'settings' && <><circle cx="12" cy="12" r="3"/><path d="m10 3-1 3-3 1-3 3 2 2-1 3 3 3 3-1 2 4 2-4 3 1 3-3-1-3 2-2-3-3-3-1-1-3z"/></>}
  </svg>
}
function initialTheme(): Theme {
  try { const value = localStorage.getItem('airgap-ui-theme'); if (value === 'light' || value === 'dark' || value === 'system') return value } catch { /* preference storage unavailable */ }
  return 'system'
}

export default function App() {
  const [tab, setTab] = useState<Tab>('home')
  const [visited, setVisited] = useState<Set<Tab>>(new Set(['home']))
  const [theme, setTheme] = useState<Theme>(initialTheme)
  const [bootstrap, setBootstrap] = useState<Bootstrap | null>(null)
  const [capsules, setCapsules] = useState<StateCapsule[]>([])
  const [overview, setOverview] = useState<AgentOverview | null>(null)
  const [fatal, setFatal] = useState('')
  const [activity, setActivity] = useState<{time: string; action: string; state: string}[]>([])
  const [activityFilter, setActivityFilter] = useState('all')
  const [activityMessage, setActivityMessage] = useState('')
  function record(action: string, state: string) { setActivity(prev => [{time: new Date().toISOString(), action, state}, ...prev].slice(0, 200)) }
  useEffect(() => onEvent<{state: string}>('download-state', event => { if (!event) return; const state = ({running: '开始下载', stopped: '下载已停止', ready: '下载完成'} as Record<string, string>)[event.state]; if (state) record('联网下载', state) }), [])
  useEffect(() => { const handler = (event: Event) => { const detail = (event as CustomEvent<string>).detail; if (['开始导入', '导入完成', '导入已停止', '已请求暂停'].includes(detail)) record('离线导入', detail) }; window.addEventListener('airgap-transfer-activity', handler); return () => window.removeEventListener('airgap-transfer-activity', handler) }, [])
  useEffect(() => { if (overview?.connected) record('服务器连接', '已连接'); else if (overview === null) record('服务器连接', '未连接') }, [overview?.connected])
  async function exportActivity() { try { const path = await app().SaveDiagnosticReport(JSON.stringify({kind:'desktop-session-activity', exportedAt:new Date().toISOString(), items: activity}, null, 2)); if (path) setActivityMessage('操作记录已保存。') } catch { setActivityMessage('保存失败。如果同名文件已存在，请换一个文件名。') } }
  function navigate(next: Tab) { setVisited(prev => new Set(prev).add(next)); setTab(next) }
  useEffect(() => {
    const media = window.matchMedia('(prefers-color-scheme: dark)')
    const apply = () => { document.documentElement.dataset.theme = theme === 'system' ? (media.matches ? 'dark' : 'light') : theme }
    apply()
    media.addEventListener('change', apply)
    try { localStorage.setItem('airgap-ui-theme', theme) } catch { /* preference storage unavailable */ }
    return () => media.removeEventListener('change', apply)
  }, [theme])
  useEffect(() => {
    app().Bootstrap().then(value => { setBootstrap(value); setCapsules(value.capsules ?? []) }).catch(e => setFatal(errorText(e)))
  }, [])
  async function reloadCapsules() { setCapsules(await app().ListCapsules() ?? []) }
  const connected = !!overview?.connected
  const sources = overview?.sources ?? []
  const themePicker = <select aria-label="界面主题" className="theme-select" value={theme} onChange={e => setTheme(e.target.value as Theme)}><option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option></select>

  if (fatal) return <main className="fatal-screen"><div className="fatal-card"><h1>AirGap Mirror</h1><p>程序没能打开本机工作目录。请检查磁盘空间和目录权限，然后重试。</p><pre>{fatal}</pre></div></main>

  return <div className="shell">
    <aside className="sidebar">
      <div className="brand"><div className="brand-mark" aria-hidden="true">AM</div><div><strong>AirGap Mirror</strong><small>离线软件源更新工具</small></div></div>
      <nav aria-label="应用导航">
        <div className="nav-label">工作台</div>
        {tabs.map((item, index) => <div className="nav-item" key={item.id}>{index === 1 && <div className="nav-label">离线更新流程</div>}{index === 4 && <div className="nav-label">管理</div>}<button className={tab === item.id ? 'active' : ''} aria-current={tab === item.id ? 'page' : undefined} onClick={() => navigate(item.id)}><NavIcon name={item.id}/><div>{item.label}<small>{item.detail}</small></div></button></div>)}
      </nav>
      <div className="sidebar-status"><div><span className={`dot ${connected ? 'ok' : ''}`} />{connected ? '服务器已连接' : '服务器未连接'}</div><small>{connected ? overview?.baseUrl : '可在未连接服务器时使用联网下载'}</small></div>
    </aside>
    <main className="content">
      <div className="desktop-topbar"><span>工作台 / {tabs.find(x => x.id === tab)?.label}</span>{themePicker}</div>
      <div hidden={tab !== 'home'} className="page-stack">
        <div className="page-header"><div><h2>概览</h2><p>在内网导出状态、在联网电脑下载更新，再将文件带回内网服务器。三个步骤可以分别在不同电脑上完成。</p></div></div>
        <div className="metrics-grid dashboard-metrics">
          <div className="metric"><span>内网服务器</span><strong>{connected ? '已连接' : '未连接'}</strong><small title={overview?.baseUrl}>{connected ? overview?.baseUrl : '联网下载无需连接服务器'}</small></div>
          <div className="metric"><span>服务器软件源</span><strong>{connected ? formatCount(sources.length) : '—'}</strong><small>{connected ? `已启用 ${sources.filter(s => s.source.enabled).length} 个` : '连接后查看'}</small></div>
          <div className="metric"><span>本机状态文件</span><strong>{bootstrap ? formatCount(capsules.length) : '—'}</strong><small>已打开或导出的状态文件</small></div>
          <div className="metric"><span>账本记录的总容量</span><strong>{connected ? formatBytes(sources.reduce((sum, s) => sum + s.state.liveBytes, 0)) : '—'}</strong><small>各软件源账本记录的文件大小之和</small></div>
        </div>
        <div className="card home-workflow"><div className="card-title"><span>开始一次离线更新</span></div><div className="workflow-grid">
          {tabs.slice(1, 4).map((item, i) => <button key={item.id} className="workflow-card" onClick={() => navigate(item.id)}><span className="workflow-step">步骤 {i + 1}</span><NavIcon name={item.id}/><strong>{item.label}</strong><p>{['从内网服务器导出状态文件。','打开状态文件，检查并下载更新。','检查下载好的批次并上传到内网服务器。'][i]}</p><span className="workflow-link">打开页面 →</span></button>)}
        </div></div>
        <div className="home-grid"><div className="card table-card"><div className="card-title"><span>最近的状态文件</span><button onClick={() => navigate('sync')}>联网下载</button></div><div className="home-capsules">{[...capsules].sort((a,b) => b.exportedAt.localeCompare(a.exportedAt)).slice(0,5).map(c => <div className="capsule-row" key={c.id}><div><strong>{shortID(c.id)}</strong><small>{formatDate(c.exportedAt)}</small></div><span>{formatCount(c.sources.length)} 个软件源</span></div>)}{!capsules.length && <p className="empty">{bootstrap ? '本机还没有状态文件。可以从服务器导出，或打开已有文件。' : '正在读取本机工作目录……'}</p>}</div></div><div className="card"><div className="card-title"><span>仓库维护</span></div><p>清点仓库文件、校验完整性、处理异常文件或下载服务器生成的状态文件。</p><div className="button-row home-maintenance"><button className="primary" onClick={() => navigate('maintenance')}>打开维护工具</button></div></div></div>
      </div>
      {visited.has('sources') && <div hidden={tab !== 'sources'}><SourceStatePage overview={overview} onOverview={setOverview} onCapsuleChanged={reloadCapsules} onMaintenance={() => navigate('maintenance')} /></div>}
      {visited.has('sync') && <div hidden={tab !== 'sync'}><InternetSyncPage capsules={capsules} onCapsules={setCapsules} /></div>}
      {visited.has('server') && <div hidden={tab !== 'server'}><ServerUpdatePage overview={overview} /></div>}
      {visited.has('maintenance') && <div hidden={tab !== 'maintenance'} className="page-stack"><div className="page-header"><div><h2>仓库维护</h2><p>查看服务器的文件清点、校验、清理和状态文件导出任务。</p></div><button onClick={() => navigate('sources')}>连接服务器</button></div>{connected ? <MaintenancePanel overview={overview} /> : <div className="card guidance"><h3>尚未连接内网服务器</h3><p>请先建立连接，再选择需要维护的软件源。</p><div className="button-row home-maintenance"><button className="primary" onClick={() => navigate('sources')}>连接服务器</button></div></div>}</div>}
      <div hidden={tab !== 'settings'} className="page-stack"><div className="page-header"><div><h2>应用设置</h2><p>调整界面外观，查看当前客户端的本机存储位置。</p></div></div><div className="card settings-card"><div><h3>界面主题</h3><p>主题仅影响本机界面，不会修改服务器配置。</p></div>{themePicker}</div><div className="card settings-card"><div><h3>本机工作目录</h3><p>保存状态文件索引、下载任务记录和其他本机工作数据。</p></div><code className="workspace-path">{bootstrap?.workspaceRoot ?? '正在读取……'}</code></div><div className="card table-card"><div className="card-title"><span>本次运行的操作记录</span><button disabled={!activity.length} onClick={exportActivity}>导出记录</button></div><div className="table-toolbar"><label>类型<select value={activityFilter} onChange={e => setActivityFilter(e.target.value)}><option value="all">全部</option><option value="服务器连接">服务器连接</option><option value="联网下载">联网下载</option><option value="离线导入">离线导入</option></select></label><span>最多保留 200 条本机操作记录，退出程序后清空。不包含服务器运行日志。</span><button disabled={!activity.length} onClick={() => { setActivity([]); setActivityMessage('已清空本机操作记录。') }}>清空</button></div>{activityMessage && <p className="activity-message" role="status">{activityMessage}</p>}<div className="table-wrap"><table><thead><tr><th>时间</th><th>操作</th><th>状态</th></tr></thead><tbody>{activity.filter(x => activityFilter === 'all' || x.action === activityFilter).map((x,i) => <tr key={x.time + i}><td>{formatDate(x.time)}</td><td>{x.action}</td><td>{x.state}</td></tr>)}</tbody></table></div>{!activity.filter(x => activityFilter === 'all' || x.action === activityFilter).length && <p className="empty">没有符合条件的操作记录。</p>}</div><div className="card"><h3>连接安全</h3><p>通过可信的 HTTPS 地址连接服务器。直接使用 HTTP 时，连接令牌不会受到传输加密保护，请仅在可信内网使用。</p></div></div>
    </main>
  </div>
}
