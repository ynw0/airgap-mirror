import { useEffect, useState } from 'react'
import { app } from './backend'
import { errorText, shortID } from './format'
import { InternetSyncPage } from './pages/InternetSyncPage'
import { ServerUpdatePage } from './pages/ServerUpdatePage'
import { SourceStatePage } from './pages/SourceStatePage'
import type { AgentOverview, Bootstrap, StateCapsule } from './types'

type Tab = 'sources' | 'sync' | 'server'

export default function App() {
  const [tab, setTab] = useState<Tab>('sources')
  const [bootstrap, setBootstrap] = useState<Bootstrap | null>(null)
  const [capsules, setCapsules] = useState<StateCapsule[]>([])
  const [overview, setOverview] = useState<AgentOverview | null>(null)
  const [fatal, setFatal] = useState('')

  useEffect(() => {
    app().Bootstrap().then(value => { setBootstrap(value); setCapsules(value.capsules ?? []) }).catch(e => setFatal(errorText(e)))
  }, [])

  async function reloadCapsules() {
    const value = await app().ListCapsules()
    setCapsules(value ?? [])
  }

  if (fatal) return <main className="fatal-screen"><div className="fatal-card"><h1>Airgap Mirror</h1><p>程序没能打开本机工作目录。请检查磁盘空间和目录权限，然后重试。</p><pre>{fatal}</pre></div></main>

  return <div className="shell">
    <aside className="sidebar">
      <div className="brand"><div className="brand-mark">AM</div><div><strong>Airgap Mirror</strong><small>离线软件源更新工具</small></div></div>
      <nav>
        <button className={tab === 'sources' ? 'active' : ''} onClick={() => setTab('sources')}><span>01</span><div>连接服务器<small>把状态文件带出去</small></div></button>
        <button className={tab === 'sync' ? 'active' : ''} onClick={() => setTab('sync')}><span>02</span><div>联网下载<small>保存到移动硬盘</small></div></button>
        <button className={tab === 'server' ? 'active' : ''} onClick={() => setTab('server')}><span>03</span><div>离线导入<small>把下载内容带回来</small></div></button>
      </nav>
      <div className="sidebar-status">
        <div><span className={`dot ${overview?.connected ? 'ok' : ''}`} />{overview?.connected ? '服务器已连接' : '服务器未连接'}</div>
        <small>本机工作目录</small><code>{bootstrap?.workspaceRoot ? shortID(bootstrap.workspaceRoot) : '初始化中…'}</code>
        <small>{capsules.length} 份状态文件</small>
      </div>
    </aside>
    <main className="content">
      {tab === 'sources' && <SourceStatePage overview={overview} onOverview={setOverview} onCapsuleChanged={reloadCapsules} />}
      {tab === 'sync' && <InternetSyncPage capsules={capsules} onCapsules={setCapsules} />}
      {tab === 'server' && <ServerUpdatePage overview={overview} />}
    </main>
  </div>
}
