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

  if (fatal) return <main className="fatal-screen"><div className="fatal-card"><h1>Airgap Mirror</h1><p>桌面工作区初始化失败。</p><pre>{fatal}</pre></div></main>

  return <div className="shell">
    <aside className="sidebar">
      <div className="brand"><div className="brand-mark">AM</div><div><strong>Airgap Mirror</strong><small>Offline Package Sync</small></div></div>
      <nav>
        <button className={tab === 'sources' ? 'active' : ''} onClick={() => setTab('sources')}><span>01</span><div>源状态<small>Agent / State Capsule</small></div></button>
        <button className={tab === 'sync' ? 'active' : ''} onClick={() => setTab('sync')}><span>02</span><div>互联网同步<small>Analyze / Download</small></div></button>
        <button className={tab === 'server' ? 'active' : ''} onClick={() => setTab('server')}><span>03</span><div>服务器更新<small>Transfer / Publish</small></div></button>
      </nav>
      <div className="sidebar-status">
        <div><span className={`dot ${overview?.connected ? 'ok' : ''}`} />{overview?.connected ? 'Mirror Agent online' : 'Mirror Agent offline'}</div>
        <small>Workspace</small><code>{bootstrap?.workspaceRoot ? shortID(bootstrap.workspaceRoot) : '初始化中…'}</code>
        <small>{capsules.length} State Capsule</small>
      </div>
    </aside>
    <main className="content">
      {tab === 'sources' && <SourceStatePage overview={overview} onOverview={setOverview} onCapsuleChanged={reloadCapsules} />}
      {tab === 'sync' && <InternetSyncPage capsules={capsules} onCapsules={setCapsules} />}
      {tab === 'server' && <ServerUpdatePage overview={overview} />}
    </main>
  </div>
}
