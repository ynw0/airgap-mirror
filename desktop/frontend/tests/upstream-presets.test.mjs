import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'

const source = readFileSync(new URL('../src/upstreamPresets.ts', import.meta.url), 'utf8')
const js = ts.transpileModule(source, {compilerOptions: {module: ts.ModuleKind.CommonJS}}).outputText
const exports = {}
vm.runInNewContext(js, {exports})
const {upstreamChoices} = exports

test('an old internal Debian address is never the default online mirror', () => {
  const source = {type:'apt', provider:'apt-generic', upstreamUrl:'http://mirror.costar.com/apt/debin', publicUrl:'http://mirror.costar.com/apt/debin', config:{suite:'bookworm-updates'}}
  const choices = upstreamChoices(source)
  assert.equal(choices[0][1], 'https://deb.debian.org/debian')
  assert.ok(choices.every(row => !row[1].includes('ubuntu') && !row[1].includes('costar.com')))
  assert.equal(source.config.suite, 'bookworm-updates')
})

test('APT security suites use the correct official archive', () => {
  for (const [suite, expected] of [['bookworm-security','https://deb.debian.org/debian-security'], ['jammy-security','https://security.ubuntu.com/ubuntu'], ['focal-updates','https://archive.ubuntu.com/ubuntu']]) {
    const choices = upstreamChoices({type:'apt',provider:'apt-generic',config:JSON.stringify({suite})})
    assert.equal(choices[0][1], expected)
  }
})

test('other built-in upstreams and custom repositories', () => {
  for (const [type, provider, expected] of [['pypi','pypi','https://pypi.org/simple'], ['npm','npmjs','https://registry.npmjs.org'], ['maven','maven-central','https://repo.maven.apache.org/maven2']]) {
    assert.equal(upstreamChoices({type,provider})[0][1], expected)
  }
  assert.equal(upstreamChoices({type:'maven',provider:'maven-generic'}).length, 0)
  assert.equal(upstreamChoices({type:'apt',provider:'apt-generic',config:{suite:'custom'}}).length, 0)
})
