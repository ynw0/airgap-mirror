import { useMemo, useState } from 'react'
import type { Source, SourceInput, SourceType } from '../types'

type Kind = 'apt' | 'pypi' | 'npm' | 'maven-generic' | 'maven-central'

type Coordinate = {
  groupId: string
  artifactId: string
  version: string
  packaging: string
  classifier: string
  original?: Record<string, unknown>
}

type Props = {
  source: Source | null
  busy: boolean
  onCancel: () => void
  onSubmit: (input: SourceInput) => Promise<void>
}

const kinds: Array<{ id: Kind; label: string; type: SourceType; provider: string }> = [
  { id: 'apt', label: 'APT', type: 'apt', provider: 'apt-generic' },
  { id: 'pypi', label: 'PyPI', type: 'pypi', provider: 'pypi' },
  { id: 'npm', label: 'npm', type: 'npm', provider: 'npmjs' },
  { id: 'maven-generic', label: 'Maven 通用仓库', type: 'maven', provider: 'maven-generic' },
  { id: 'maven-central', label: 'Maven Central 指定依赖', type: 'maven', provider: 'maven-central' },
]

const upstreamPresets: Record<Kind, string[][]> = {
  "apt": [
    [
      "Debian 官方",
      "https://deb.debian.org/debian",
      "bookworm"
    ],
    [
      "Debian 安全更新",
      "https://deb.debian.org/debian-security",
      "bookworm-security"
    ],
    [
      "Ubuntu 官方",
      "https://archive.ubuntu.com/ubuntu",
      "noble"
    ],
    [
      "Ubuntu 安全更新",
      "https://security.ubuntu.com/ubuntu",
      "noble-security"
    ],
    [
      "清华大学 · Debian",
      "https://mirrors.tuna.tsinghua.edu.cn/debian",
      "bookworm"
    ],
    [
      "清华大学 · Ubuntu",
      "https://mirrors.tuna.tsinghua.edu.cn/ubuntu",
      "noble"
    ],
    [
      "中科大 · Debian",
      "https://mirrors.ustc.edu.cn/debian",
      "bookworm"
    ],
    [
      "中科大 · Ubuntu",
      "https://mirrors.ustc.edu.cn/ubuntu",
      "noble"
    ],
    [
      "阿里云 · Debian",
      "https://mirrors.aliyun.com/debian",
      "bookworm"
    ],
    [
      "阿里云 · Ubuntu",
      "https://mirrors.aliyun.com/ubuntu",
      "noble"
    ],
    [
      "华为云 · Debian",
      "https://mirrors.huaweicloud.com/debian",
      "bookworm"
    ],
    [
      "华为云 · Ubuntu",
      "https://mirrors.huaweicloud.com/ubuntu",
      "noble"
    ]
  ],
  "pypi": [
    [
      "PyPI 官方",
      "https://pypi.org/simple"
    ],
    [
      "清华大学",
      "https://mirrors.tuna.tsinghua.edu.cn/pypi/web/simple"
    ],
    [
      "中科大",
      "https://mirrors.ustc.edu.cn/pypi/simple"
    ],
    [
      "阿里云",
      "https://mirrors.aliyun.com/pypi/simple"
    ],
    [
      "华为云",
      "https://mirrors.huaweicloud.com/repository/pypi/simple"
    ]
  ],
  "npm": [
    [
      "npm 官方",
      "https://registry.npmjs.org"
    ],
    [
      "npmmirror",
      "https://registry.npmmirror.com"
    ],
    [
      "华为云",
      "https://mirrors.huaweicloud.com/repository/npm"
    ]
  ],
  "maven-central": [
    [
      "Maven Central 官方",
      "https://repo.maven.apache.org/maven2"
    ],
    [
      "Maven Central 备用官方地址",
      "https://repo1.maven.org/maven2"
    ],
    [
      "阿里云 Central 镜像",
      "https://maven.aliyun.com/repository/central"
    ],
    [
      "中科大代理",
      "https://maven.proxy.ustclug.org/maven2"
    ]
  ],
  "maven-generic": []
}

function asObject(value: unknown): Record<string, unknown> {
  if (typeof value === 'string') {
    try {
      const parsed = JSON.parse(value)
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) return parsed as Record<string, unknown>
    } catch { /* 非 JSON 字符串按空对象处理，提交时由 Agent ValidateConfig 拒绝 */ }
    return {}
  }
  if (value && typeof value === 'object' && !Array.isArray(value)) return value as Record<string, unknown>
  return {}
}

function kindOf(type: string, provider: string): Kind {
  const found = kinds.find(k => k.type === type && k.provider === provider)
  return found?.id ?? 'apt'
}

function emptyCoordinate(): Coordinate {
  return { groupId: '', artifactId: '', version: '', packaging: 'jar', classifier: '' }
}

function coordinatesFrom(config: Record<string, unknown>): Coordinate[] {
  const raw = config.coordinates
  if (!Array.isArray(raw) || raw.length === 0) return [emptyCoordinate()]
  return raw.map(item => {
    const row = (item && typeof item === 'object') ? item as Record<string, unknown> : {}
    return {
      groupId: String(row.groupId ?? ''),
      artifactId: String(row.artifactId ?? ''),
      version: String(row.version ?? ''),
      packaging: String(row.packaging ?? 'jar') || 'jar',
      classifier: String(row.classifier ?? ''),
      original: row,
    }
  })
}

export function SourceEditor({ source, busy, onCancel, onSubmit }: Props) {
  const editing = !!source
  const initial = asObject(source?.config)
  const [validation, setValidation] = useState('')
  const [kind, setKind] = useState<Kind>(source ? kindOf(source.type, source.provider) : 'apt')
  const [name, setName] = useState(source?.name ?? '')
  const [upstreamUrl, setUpstreamUrl] = useState(source?.upstreamUrl ?? upstreamPresets.apt[0][1])
  const [rootPath, setRootPath] = useState(source?.rootPath ?? '')
  const [publicUrl, setPublicUrl] = useState(source?.publicUrl ?? '')
  const [enabled, setEnabled] = useState(source?.enabled ?? true)
  const [suite, setSuite] = useState(String(initial.suite ?? 'bookworm'))
  const [releaseMode, setReleaseMode] = useState(String(initial.releaseMode ?? 'inrelease'))
  const [gcOwnsPool, setGcOwnsPool] = useState(initial.gcOwnsPool === true)
  const [requireValidUntil, setRequireValidUntil] = useState(initial.requireValidUntil === true)
  const [replicationUrl, setReplicationUrl] = useState(String(initial.replicationUrl ?? 'https://replicate.npmjs.com/registry'))
  const [changePageSize, setChangePageSize] = useState(Number(initial.changePageSize ?? 10000))
  const [allDocsPageSize, setAllDocsPageSize] = useState(Number(initial.allDocsPageSize ?? 10000))
  const [indexUrl, setIndexUrl] = useState(String(initial.indexUrl ?? ''))
  const [inventoryMode, setInventoryMode] = useState(String(initial.inventoryMode ?? 'filesystem'))
  const [coordinates, setCoordinates] = useState<Coordinate[]>(coordinatesFrom(initial))

  const spec = useMemo(() => kinds.find(k => k.id === kind) ?? kinds[0], [kind])

  function changeKind(next: Kind) {
    if (editing) return
    setKind(next)
    setValidation('')
    const known = Object.values(upstreamPresets).flat().some(p => p[1] === upstreamUrl)
    if (!upstreamUrl.trim() || known) setUpstreamUrl(upstreamPresets[next][0]?.[1] ?? '')
    if (next === 'apt') { setSuite('bookworm'); setReleaseMode('inrelease'); setGcOwnsPool(false) }
    if (next === 'npm') { setReplicationUrl('https://replicate.npmjs.com/registry'); setChangePageSize(10000); setAllDocsPageSize(10000) }
    if (next === 'maven-generic') { setInventoryMode('filesystem') }
    if (next === 'maven-central' && coordinates.length === 0) setCoordinates([emptyCoordinate()])
  }

  function config(): unknown {
    const preserved = editing ? initial : {}
    switch (kind) {
      case 'apt':
        return { ...preserved, suite: suite.trim(), releaseMode, gcOwnsPool, requireValidUntil }
      case 'pypi':
        return { ...preserved }
      case 'npm':
        return { ...preserved, replicationUrl: replicationUrl.trim(), changePageSize, allDocsPageSize }
      case 'maven-generic':
        return { ...preserved, indexUrl: indexUrl.trim(), inventoryMode: inventoryMode.trim() }
      case 'maven-central':
        return {
          ...preserved,
          mode: 'dependency-set',
          coordinates: coordinates
            .map(c => ({
              ...c.original,
              groupId: c.groupId.trim(),
              artifactId: c.artifactId.trim(),
              version: c.version.trim(),
              packaging: c.packaging.trim() || 'jar',
              classifier: c.classifier.trim() || undefined,
            }))
            .filter(c => c.groupId || c.artifactId || c.version),
        }
    }
  }

  async function submit() {
    setValidation('')
    if (busy) return
    if (source && !kinds.some(k => k.type === source.type && k.provider === source.provider)) { setValidation('当前客户端不支持修改此软件源类型。'); return }
    if (typeof source?.config === 'string') { try { const v: unknown = JSON.parse(source.config); if (!v || typeof v !== 'object' || Array.isArray(v)) throw new Error() } catch { setValidation('原有软件源配置不是有效的 JSON 对象，不能安全保存。请先在服务器上检查配置。'); return } }
    if (!name.trim() || !upstreamUrl.trim() || !rootPath.trim() || !publicUrl.trim()) { setValidation('请填写软件源名称、公网下载地址、服务器存放目录和内网访问地址。'); return }
    if (kind === 'apt' && !suite.trim()) { setValidation('请填写 APT 发行版代号。'); return }
    if (kind === 'npm' && [changePageSize, allDocsPageSize].some(n => !Number.isInteger(n) || n < 1 || n > 10000)) { setValidation('npm 每次读取的数量必须为 1 至 10000 的整数。'); return }
    if (kind === 'maven-central' && (!coordinates.some(c => c.groupId.trim() && c.artifactId.trim() && c.version.trim()) || coordinates.some(c => (c.groupId.trim() || c.artifactId.trim() || c.version.trim()) && !(c.groupId.trim() && c.artifactId.trim() && c.version.trim())))) { setValidation('请至少填写一项完整的 Maven 依赖，并为每项依赖填写 Group ID、Artifact ID 和版本。'); return }
    await onSubmit({
      ...(source?.id ? { id: source.id } : {}),
      name: name.trim(),
      type: spec.type,
      provider: spec.provider,
      upstreamUrl: upstreamUrl.trim(),
      rootPath: rootPath.trim(),
      publicUrl: publicUrl.trim(),
      enabled,
      config: config(),
    })
  }

  return <div className="card source-editor">
    <div className="card-title">
      <span>{editing ? '修改软件源设置' : '添加软件源'}</span>
      {source ? <code>{source.id}</code> : <span>创建后会自动生成编号</span>}
    </div>
    <div className="editor-section-heading"><h3>基本信息</h3><p>指定公网软件源、内网仓库目录及客户端访问地址。</p></div>
    <fieldset disabled={busy} className="editor-fieldset"><div className="form-grid source-form">
      <label>软件源名称<input value={name} onChange={e => setName(e.target.value)} /></label>
      <label>软件类型
        <select disabled={editing || busy} value={kind} onChange={e => changeKind(e.target.value as Kind)}>
          {kinds.map(k => <option key={k.id} value={k.id}>{k.label} · {k.provider}</option>)}
        </select>
      </label>
      <label className="checkbox">启用这个软件源
        <input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} />
      </label>
      <label className="span-2">公网下载地址{upstreamPresets[kind].length > 0 && <select aria-label="选择常用公网下载地址" value="" onChange={e => { const preset = upstreamPresets[kind].find(p => p[1] === e.target.value); if (preset) { setUpstreamUrl(preset[1]); if (!editing && preset[2]) setSuite(preset[2]) } }}><option value="">选择官方源或镜像站</option>{upstreamPresets[kind].map(p => <option key={p[1]} value={p[1]}>{p[0]}</option>)}</select>}<input value={upstreamUrl} onChange={e => setUpstreamUrl(e.target.value)} placeholder={
        kind === 'apt' ? 'https://deb.debian.org/debian' :
        kind === 'pypi' ? 'https://pypi.org/simple' :
        kind === 'npm' ? 'https://registry.npmjs.org' :
        kind === 'maven-central' ? 'https://repo.maven.apache.org/maven2' :
        'https://upstream.example/repository'
      } />{(kind === 'pypi' || kind === 'npm') && <small>镜像站可能不支持完整仓库同步所需的接口，请在联网电脑上先检查上游状态。</small>}{editing && upstreamUrl !== source?.upstreamUrl && <small>已有同步记录的软件源可能不允许直接更换地址。</small>}</label>
      <label>服务器存放目录<input value={rootPath} onChange={e => setRootPath(e.target.value)} placeholder="/srv/mirror/..." /></label>
      <label className="span-2">内网访问地址<input value={publicUrl} onChange={e => setPublicUrl(e.target.value)} placeholder="https://repo.internal/..." /></label>
      </div><div className="editor-section-heading"><h3>软件源专用配置</h3></div><div className="form-grid source-form">
      {kind === 'pypi' && <p className="span-2">PyPI 无需额外配置。</p>}
      {kind === 'apt' && <>
        <label>发行版代号<input list="apt-suites" value={suite} onChange={e => setSuite(e.target.value)} /><datalist id="apt-suites">{["focal", "jammy", "noble", "resolute", "bullseye", "bookworm", "trixie", "stable", "testing", "sid", "focal-updates", "focal-security", "jammy-updates", "jammy-security", "noble-updates", "noble-security", "bookworm-security", "trixie-security"].map(s => <option key={s} value={s}/>)}</datalist></label>
        <label>Release 签名方式
          <select value={releaseMode} onChange={e => setReleaseMode(e.target.value)}>
            <option value="inrelease">InRelease（内嵌签名）</option>
            <option value="detached">Release.gpg（分离签名）</option>
          </select>
        </label>
        <p className="span-2 small">{initial.trustedKeyringBase64 ? '使用此软件源已有的签名公钥配置。' : 'Ubuntu 和 Debian 使用程序内置的官方签名公钥，无需另外配置。'}</p>
        <label className="checkbox">要求上游 Release 包含 Valid-Until
          <input type="checkbox" checked={requireValidUntil} onChange={e => setRequireValidUntil(e.target.checked)} />
        </label>
        <label className="checkbox">允许清理该软件源独占的包池
          <input type="checkbox" checked={gcOwnsPool} onChange={e => setGcOwnsPool(e.target.checked)} />
        </label>
      </>}
      {kind === 'npm' && <>
        <label className="span-2">npm 更新记录地址<input value={replicationUrl} onChange={e => setReplicationUrl(e.target.value)} /></label>
        <label>每次读取的更新数<input type="number" min={1} max={10000} value={changePageSize} onChange={e => setChangePageSize(Number(e.target.value))} /></label>
        <label>每次读取的软件包数<input type="number" min={1} max={10000} value={allDocsPageSize} onChange={e => setAllDocsPageSize(Number(e.target.value))} /></label>
      </>}
      {kind === 'maven-generic' && <>
        <label className="span-2">仓库文件清单地址<input value={indexUrl} onChange={e => setIndexUrl(e.target.value)} placeholder="https://upstream.example/mirror-index.ndjson" /></label>
        <label>本地扫描方式
          <select value={inventoryMode} onChange={e => setInventoryMode(e.target.value)}>
            <option value="filesystem">扫描仓库目录</option>
          </select>
        </label>
      </>}
    </div>
    {kind === 'maven-central' && <div className="coord-block">
      <div className="card-title"><span>要下载的 Maven 依赖（填写精确版本）</span><button type="button" onClick={() => setCoordinates(prev => [...prev, emptyCoordinate()])}>添加依赖</button></div>
      <div className="table-wrap"><table><thead><tr><th>Group ID</th><th>Artifact ID</th><th>版本</th><th>打包类型</th><th>分类标识（可选）</th><th></th></tr></thead>
        <tbody>{coordinates.map((c, i) => <tr key={i}>
          <td><input aria-label={`第 ${i + 1} 项依赖：Group ID`} value={c.groupId} onChange={e => setCoordinates(prev => prev.map((x, n) => n === i ? { ...x, groupId: e.target.value } : x))} /></td>
          <td><input aria-label={`第 ${i + 1} 项依赖：Artifact ID`} value={c.artifactId} onChange={e => setCoordinates(prev => prev.map((x, n) => n === i ? { ...x, artifactId: e.target.value } : x))} /></td>
          <td><input aria-label={`第 ${i + 1} 项依赖：版本`} value={c.version} onChange={e => setCoordinates(prev => prev.map((x, n) => n === i ? { ...x, version: e.target.value } : x))} /></td>
          <td><input aria-label={`第 ${i + 1} 项依赖：打包类型`} value={c.packaging} onChange={e => setCoordinates(prev => prev.map((x, n) => n === i ? { ...x, packaging: e.target.value } : x))} /></td>
          <td><input aria-label={`第 ${i + 1} 项依赖：分类标识`} value={c.classifier} onChange={e => setCoordinates(prev => prev.map((x, n) => n === i ? { ...x, classifier: e.target.value } : x))} /></td>
          <td><button type="button" className="danger-ghost" disabled={coordinates.length === 1} onClick={() => setCoordinates(prev => prev.filter((_, n) => n !== i))}>删除</button></td>
        </tr>)}</tbody>
      </table></div>
    </div>}
    </fieldset>
    {validation && <div className="notice editor-error" role="alert">{validation}</div>}
    <div className="button-row editor-footer">
      <button className="primary" disabled={busy} onClick={() => void submit()}>{editing ? '保存修改' : '添加软件源'}</button>
      <button disabled={busy} onClick={onCancel}>取消</button>
    </div>
  </div>
}
