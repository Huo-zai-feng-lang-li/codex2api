import assert from 'node:assert/strict'
import { Buffer } from 'node:buffer'
import fs from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

async function loadModule(name) {
  const url = new URL(name, import.meta.url)
  const source = fs.readFileSync(url, 'utf8')
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.ES2022,
      target: ts.ScriptTarget.ES2022,
    },
  }).outputText
  return import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`)
}

const proxyUrl = await loadModule('./proxyUrl.ts')
const zh = JSON.parse(fs.readFileSync(new URL('../locales/zh.json', import.meta.url), 'utf8'))
const en = JSON.parse(fs.readFileSync(new URL('../locales/en.json', import.meta.url), 'utf8'))

test('exports the local proxy default used by proxy forms', () => {
  assert.equal(proxyUrl.DEFAULT_PROXY_URL, 'http://127.0.0.1:51081')
})

test('defines empty credential placeholders instead of sample credential defaults', () => {
  assert.equal(zh.proxyInput.usernamePlaceholder, '请填写代理 IP 用户名')
  assert.equal(zh.proxyInput.passwordPlaceholder, '请填写代理 IP 密码')
  assert.equal(en.proxyInput.usernamePlaceholder, 'Proxy IP username')
  assert.equal(en.proxyInput.passwordPlaceholder, 'Proxy IP password')
})

test('builds an authenticated HTTP proxy URL from separate fields', () => {
  assert.equal(proxyUrl.buildProxyUrl({
    scheme: 'http',
    host: 'c489.fxip.cc',
    port: '9345',
    username: '3601999',
    password: 'aba2f382c',
  }), 'http://3601999:aba2f382c@c489.fxip.cc:9345')
})

test('encodes credentials when building proxy URLs', () => {
  assert.equal(proxyUrl.buildProxyUrl({
    scheme: 'socks5',
    host: 'proxy.example.com',
    port: '1080',
    username: 'name@example.com',
    password: 'p@ss:word',
  }), 'socks5://name%40example.com:p%40ss%3Aword@proxy.example.com:1080')
})

test('parses an authenticated proxy URL into editable fields', () => {
  assert.deepEqual(proxyUrl.parseProxyUrl('https://user:p%40ss%3Aword@proxy.example.com:8443'), {
    scheme: 'https',
    host: 'proxy.example.com',
    port: '8443',
    username: 'user',
    password: 'p@ss:word',
  })
})

test('keeps explicitly typed default ports editable', () => {
  assert.deepEqual(proxyUrl.parseProxyUrl('http://proxy.example.com:80'), {
    scheme: 'http',
    host: 'proxy.example.com',
    port: '80',
    username: '',
    password: '',
  })
})

test('validates supported proxy URL schemes', () => {
  assert.equal(proxyUrl.isValidProxyUrl('http://user:pass@proxy.example.com:8080'), true)
  assert.equal(proxyUrl.isValidProxyUrl('socks5h://proxy.example.com:1080'), true)
  assert.equal(proxyUrl.isValidProxyUrl('ftp://proxy.example.com:21'), false)
  assert.equal(proxyUrl.isValidProxyUrl('', true), true)
  assert.equal(proxyUrl.isValidProxyUrl('', false), false)
})

test('requires a host port for concrete proxy URLs', () => {
  assert.equal(proxyUrl.isValidProxyUrl('http://proxy.example.com'), false)
  assert.equal(proxyUrl.isValidProxyUrl('http://proxy.example.com:8080'), true)
})

test('requires proxy credentials to be filled as a pair', () => {
  assert.equal(proxyUrl.isValidProxyUrl('http://user@proxy.example.com:8080'), false)
  assert.equal(proxyUrl.isValidProxyUrl('http://:pass@proxy.example.com:8080'), false)
  assert.equal(proxyUrl.isValidProxyUrl('http://user:pass@proxy.example.com:8080'), true)
})
