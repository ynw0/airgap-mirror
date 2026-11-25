import { useMemo, useState } from 'react'
import type { Source, SourceInput, SourceType } from '../types'

type Kind = 'apt' | 'pypi' | 'npm' | 'maven-generic' | 'maven-central'

type Coordinate = {
  groupId: string
  artifactId: string
  version: string
  packaging: string
  classifier: string
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
  { id: 'maven-generic', label: 'Maven Generic', type: 'maven', provider: 'maven-generic' },
  { id: 'maven-central', label: 'Maven Central', type: 'maven', provider: 'maven-central' },
]

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
    }
  })
}

export function SourceEditor({ source, busy, onCancel, onSubmit }: Props) {
  const editing = !!source
  const initial = asObject(source?.config)
  const [kind, setKind] = useState<Kind>(source ? kindOf(source.type, source.provider) : 'apt')
  const [name, setName] = useState(source?.name ?? '')
  const [upstreamUrl, setUpstreamUrl] = useState(source?.upstreamUrl ?? '')
  const [rootPath, setRootPath] = useState(source?.rootPath ?? '')
  const [publicUrl, setPublicUrl] = useState(source?.publicUrl ?? '')
  const [enabled, setEnabled] = useState(source?.enabled ?? true)
  const [suite, setSuite] = useState(String(initial.suite ?? 'bookworm'))
  const [releaseMode, setReleaseMode] = useState(String(initial.releaseMode ?? 'inrelease'))
  const [gcOwnsPool, setGcOwnsPool] = useState(initial.gcOwnsPool === true)
  const [trustedKeyring, setTrustedKeyring] = useState(String(initial.trustedKeyringBase64 ?? ''))
  const [requireValidUntil, setRequireValidUntil] = useState(initial.requireValidUntil === true)
  const [replicationUrl, setReplicationUrl] = useState(String(initial.replicationUrl ?? 'https://replicate.npmjs.com/registry'))
  const [changePageSize, setChangePageSize] = useState(Number(initial.changePageSize ?? 10000))
  const [allDocsPageSize, setAllDocsPageSize] = useState(Number(initial.allDocsPageSize ?? 10000))
  const [indexUrl, setIndexUrl] = useState(String(initial.indexUrl ?? ''))
  const [inventoryMode, setInventoryMode] = useState(String(initial.inventoryMode ?? 'filesystem'))
  const [coordinates, setCoordinates] = useState<Coordinate[]>(coordinatesFrom(initial))

  const spec = useMemo(() => kinds.find(k => k.id === kind) ?? kinds[0], [kind])

  function changeKind(next: Kind) {
    setKind(next)
    if (editing) return
    if (next === 'apt') { setSuite('bookworm'); setReleaseMode('inrelease'); setGcOwnsPool(false) }
    if (next === 'npm') { setReplicationUrl('https://replicate.npmjs.com/registry'); setChangePageSize(10000); setAllDocsPageSize(10000) }
    if (next === 'maven-generic') { setInventoryMode('filesystem') }
    if (next === 'maven-central' && coordinates.length === 0) setCoordinates([emptyCoordinate()])
  }

  function config(): unknown {
    switch (kind) {
      case 'apt':
        return { suite: suite.trim(), releaseMode, gcOwnsPool, trustedKeyringBase64: trustedKeyring.trim(), requireValidUntil }
      case 'pypi':
        return {}
      case 'npm':
        return { replicationUrl: replicationUrl.trim(), changePageSize, allDocsPageSize }
      case 'maven-generic':
        return { indexUrl: indexUrl.trim(), inventoryMode: inventoryMode.trim() }
      case 'maven-central':
        return {
          mode: 'dependency-set',
          coordinates: coordinates
            .map(c => ({
              groupId: c.groupId.trim(),
              artifactId: c.artifactId.trim(),
              version: c.version.trim(),
              packaging: c.packaging.trim() || 'jar',
              ...(c.classifier.trim() ? { classifier: c.classifier.trim() } : {}),
            }))
            .filter(c => c.groupId || c.artifactId || c.version),
        }
    }
  }

  async function submit() {
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
    <div className="form-grid source-form">
      <label>软件源名称<input value={name} onChange={e => setName(e.target.value)} /></label>
      <label>软件类型
        <select value={kind} onChange={e => changeKind(e.target.value as Kind)}>
          {kinds.map(k => <option key={k.id} value={k.id}>{k.label} · {k.provider}</option>)}
        </select>
      </label>
      <label className="checkbox">启用这个软件源
        <input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} />
      </label>
      <label className="span-2">公网下载地址<input value={upstreamUrl} onChange={e => setUpstreamUrl(e.target.value)} placeholder={
        kind === 'apt' ? 'https://deb.debian.org/debian' :
        kind === 'pypi' ? 'https://pypi.org/simple' :
        kind === 'npm' ? 'https://registry.npmjs.org' :
        kind === 'maven-central' ? 'https://repo.maven.apache.org/maven2' :
        'https://upstream.example/repository'
      } /></label>
      <label>服务器存放目录<input value={rootPath} onChange={e => setRootPath(e.target.value)} placeholder="/srv/mirror/..." /></label>
      <label className="span-2">内网访问地址<input value={publicUrl} onChange={e => setPublicUrl(e.target.value)} placeholder="https://repo.internal/..." /></label>
      {kind === 'apt' && <>
        <label>发行版代号（suite）<input value={suite} onChange={e => setSuite(e.target.value)} /></label>
        <label>Release 签名方式
          <select value={releaseMode} onChange={e => setReleaseMode(e.target.value)}>
            <option value="inrelease">inrelease</option>
            <option value="detached">detached</option>
          </select>
        </label>
        <label className="span-2">可信上游 OpenPGP 公钥环（Base64）
          <textarea rows={4} spellCheck={false} value={trustedKeyring} onChange={e => setTrustedKeyring(e.target.value)} placeholder="由管理员从可信发行版渠道取得的二进制公钥环，转为 Base64 后粘贴" />
          <small>不要填写私钥。未配置公钥环时，联网端拒绝 APT 上游分析；联网电脑需要安装 GnuPG。</small>
        </label>
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
            <option value="filesystem">filesystem</option>
          </select>
        </label>
      </>}
    </div>
    {kind === 'maven-central' && <div className="coord-block">
      <div className="card-title"><span>要下载的 Maven 依赖（填写精确版本）</span><button type="button" onClick={() => setCoordinates(prev => [...prev, emptyCoordinate()])}>添加坐标</button></div>
      <div className="table-wrap"><table><thead><tr><th>groupId</th><th>artifactId</th><th>version</th><th>packaging</th><th>classifier</th><th></th></tr></thead>
        <tbody>{coordinates.map((c, i) => <tr key={i}>
          <td><input value={c.groupId} onChange={e => setCoordinates(prev => prev.map((x, n) => n === i ? { ...x, groupId: e.target.value } : x))} /></td>
          <td><input value={c.artifactId} onChange={e => setCoordinates(prev => prev.map((x, n) => n === i ? { ...x, artifactId: e.target.value } : x))} /></td>
          <td><input value={c.version} onChange={e => setCoordinates(prev => prev.map((x, n) => n === i ? { ...x, version: e.target.value } : x))} /></td>
          <td><input value={c.packaging} onChange={e => setCoordinates(prev => prev.map((x, n) => n === i ? { ...x, packaging: e.target.value } : x))} /></td>
          <td><input value={c.classifier} onChange={e => setCoordinates(prev => prev.map((x, n) => n === i ? { ...x, classifier: e.target.value } : x))} /></td>
          <td><button type="button" className="danger-ghost" disabled={coordinates.length === 1} onClick={() => setCoordinates(prev => prev.filter((_, n) => n !== i))}>删除</button></td>
        </tr>)}</tbody>
      </table></div>
    </div>}
    <div className="button-row">
      <button className="primary" disabled={busy} onClick={() => void submit()}>{editing ? '保存修改' : '添加软件源'}</button>
      <button disabled={busy} onClick={onCancel}>取消</button>
    </div>
  </div>
}
