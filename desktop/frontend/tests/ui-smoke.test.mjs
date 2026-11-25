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
  assert.match(message(404), /子路径/)
  assert.match(message(502), /代理/)
  assert.match(message(500), /HTTP 500/)
})

test('management URLs stay relative to the deployment subpath', () => {
  assert.match(script, /const basePath=window\.location\.pathname\.replace/)
  assert.match(script, /fetch\(basePath\+path/)
  for (const link of ['downloads/configure-agent.sh', 'downloads/configure-apt-signing.sh']) assert.ok(html.includes('href="' + link + '"'))
})
