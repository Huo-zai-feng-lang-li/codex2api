import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const source = fs.readFileSync(new URL('./Layout.tsx', import.meta.url), 'utf8')
const versionTemplate = source.match(/<span>(v\{[^<]+\})<\/span>/)?.[1]

assert.ok(versionTemplate, 'Layout must contain the version label')

const renderVersion = Function('__APP_VERSION__', `return \`${versionTemplate.replace(/\{([^{}]+)\}/, '${$1}')}\``)

test('renders one version prefix for tagged, untagged, and dev versions', () => {
  assert.equal(renderVersion('v2.2.7'), 'v2.2.7')
  assert.equal(renderVersion('2.2.7'), 'v2.2.7')
  assert.equal(renderVersion('dev'), 'vdev')
})
