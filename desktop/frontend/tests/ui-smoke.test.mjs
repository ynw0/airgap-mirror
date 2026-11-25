import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'

const html = readFileSync(new URL('../../../internal/server/web/index.html', import.meta.url), 'utf8')
const script = html.match(/<script>([\s\S]*?)<\/script>/)?.[1]
assert.ok(script, 'embedded management script exists')

function functions(start, end) {
  const first = script.indexOf(start), last = script.indexOf(end, first + 1)
  assert.ok(first >= 0 && last > first, 'expected helper boundaries exist')
  return script.slice(first, last)
}

test('Web UI script parses and references only existing unique IDs', () => {
  new vm.Script(script)
  const ids = [...html.matchAll(/\bid="([^"]+)"/g)].map(m => m[1])
  assert.equal(new Set(ids).size, ids.length)
  for (const [, id] of script.matchAll(/\bel\('([^']+)'\)/g)) assert.ok(ids.includes(id), 'missing element: ' + id)
  for (const [, view] of html.matchAll(/data-view="([^"]+)"/g)) assert.ok(ids.includes(view), 'missing navigation target: ' + view)
})

test('capacity warnings have correct boundaries and invalid data remains unknown', () => {
  const usage = vm.runInNewContext(functions('function diskUsage(', 'function diskCard(') + ';diskUsage')
  for (const [free, level] of [[100, 'normal'], [16, 'normal'], [15, 'warning'], [6, 'warning'], [5, 'critical'], [0, 'critical']]) {
    assert.equal(usage({ totalBytes: 100, freeBytes: free }).level, level)
  }
  for (const input of [null, {}, {totalBytes: 0, freeBytes: 0}, {totalBytes: 100, freeBytes: 101}, {totalBytes: 100, freeBytes: -1}]) assert.equal(usage(input), null)
})

test('visual config preserves extension fields and rejects invalid values', () => {
  const parse = vm.runInNewContext(functions('function parseConfig(', 'function editorError(') + ';parseConfig')
  const validate = vm.runInNewContext(functions('function validateConfig(', 'function openEditor(') + ';validateConfig')
  assert.equal(parse('{"suite":"bookworm","custom":{"x":1}}').custom.x, 1)
  for (const value of ['[]', 'null', 'bad']) assert.throws(() => parse(value))
  assert.doesNotThrow(() => validate('apt', {suite:'bookworm'}))
  assert.doesNotThrow(() => validate('npm', {changePageSize:1,allDocsPageSize:10000}))
  assert.doesNotThrow(() => validate('maven-central', {coordinates:[{groupId:'a',artifactId:'b',version:'1'}]}))
  for (const [kind, config] of [['apt',{suite:''}],['npm',{changePageSize:0}],['npm',{changePageSize:10001}],['maven-central',{coordinates:[]}],['maven-central',{coordinates:[{groupId:'a',artifactId:'b'}]}]]) assert.throws(() => validate(kind,config))
})

test('connection diagnostics distinguish access, proxy path and gateway failures', () => {
  const message = vm.runInNewContext(functions('function diagnosticMessage(', 'async function diagnoseConnection(') + ';diagnosticMessage')
  assert.match(message(401), /令牌/)
  assert.match(message(403), /权限/)
  assert.match(message(404), /反向代理路径/)
  assert.match(message(502), /代理/)
  assert.match(message(500), /HTTP 500/)
})

test('management URLs stay relative to the deployment subpath', () => {
  assert.match(script, /const basePath=window\.location\.pathname\.replace/)
  assert.match(script, /fetch\(basePath\+path/)
  for (const link of ['downloads/configure-agent.sh', 'downloads/configure-apt-signing.sh']) assert.ok(html.includes('href="' + link + '"'))
})

test('report path comparator follows UTF-8 byte ordering, including non-ASCII paths', () => {
  const compare = vm.runInNewContext(functions('function compareReportPaths(', 'async function exportFullRepair(') + ';compareReportPaths', {TextEncoder})
  assert.ok(compare('a', 'b') < 0)
  assert.ok(compare('a', 'aa') < 0)
  assert.equal(compare('same', 'same'), 0)
  assert.ok(compare('包/文件', '包/文件2') < 0)
  assert.ok(compare('\uE000', '\u{10000}') < 0)
})


test('overview distinguishes unavailable capacity from a normal disk', () => {
  const nodes = new Map()
  function make(tag, text = '') {
    return {tag, text, children: [], append(...items) { this.children.push(...items) }, appendChild(item) { this.children.push(item) }, replaceChildren(...items) { this.children = items }}
  }
  const el = id => { if (!nodes.has(id)) nodes.set(id, make('div')); return nodes.get(id) }
  const sources = [{id:'a', name:'APT', type:'apt', enabled:true}, {id:'b', name:'PyPI', type:'pypi', enabled:true}]
  const repositorySnapshots = new Map(sources.map(s => [s.id, {liveObjects:1, liveBytes:10}]))
  const capacitySnapshots = new Map([['a', {totalBytes:100, freeBytes:50}]])
  const render = vm.runInNewContext(functions('function diskUsage(', 'function diskCard(') + functions('function renderOverview(', 'const viewInfo=') + ';renderOverview', {
    sources, repositorySnapshots, capacitySnapshots, el, make, metric:(label,value) => ({label,value}), number:String, bytes:String, action:label=>make('button', label)
  })
  render()
  assert.equal(el('overview').children.at(-1).value, '0（1 个未读取）')
  assert.match(JSON.stringify(el('repositorySummary')), /磁盘容量未读取/)
  capacitySnapshots.set('b', {totalBytes:100, freeBytes:5})
  render()
  assert.equal(el('overview').children.at(-1).value, '1')
  assert.doesNotMatch(JSON.stringify(el('repositorySummary')), /磁盘容量未读取/)
})


test('task center offers the appropriate maintenance actions', async () => {
  const requests = [], notices = []
  let confirmed = false
  const controls = vm.runInNewContext(functions('function taskControls(', 'function renderTasks(') + ';taskControls', {
    make: () => ({children: [], appendChild(child) { this.children.push(child) }}),
    action: (label, run) => ({label, run}),
    api: async (path, options) => requests.push([path, options.method]),
    confirm: () => confirmed,
    show: message => notices.push(message),
    refreshTasks: async () => {}
  })
  const labels = (source, job) => controls(source, job).children.map(b => b.label).join(',')
  const pypi = {type:'pypi'}, apt = {type:'apt'}
  const running = {id:'job1', kind:'INVENTORY', status:'RUNNING'}
  assert.equal(labels(pypi, running), '暂停,取消任务')
  assert.equal(labels(apt, running), '取消任务')
  assert.equal(labels(pypi, {...running, status:'PAUSED'}), '继续,放弃任务')
  assert.equal(labels(pypi, {...running, status:'COMPLETED'}), '')
  await controls(pypi, running).children[0].run()
  assert.equal(requests[0].join(' '), '/api/v1/maintenance/job1/pause POST')
  assert.match(notices[0], /暂停/)
  const paused = controls(pypi, {...running, status:'PAUSED'})
  await paused.children[0].run()
  assert.equal(requests[1][0], '/api/v1/maintenance/job1/resume')
  await paused.children[1].run()
  assert.equal(requests.length, 2)
  confirmed = true
  await paused.children[1].run()
  assert.equal(requests[2][0], '/api/v1/maintenance/job1/cancel')
})
