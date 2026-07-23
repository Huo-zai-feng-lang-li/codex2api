import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const source = fs.readFileSync(new URL('./UsageStatsSummary.tsx', import.meta.url), 'utf8')
const zh = JSON.parse(fs.readFileSync(new URL('../locales/zh.json', import.meta.url), 'utf8'))
const en = JSON.parse(fs.readFileSync(new URL('../locales/en.json', import.meta.url), 'utf8'))

test('provides an accessible yellow RPM and TPM explanation tooltip', () => {
  assert.match(zh.dashboard.rpmTpmHelp, /最近 60 秒/)
  assert.match(en.dashboard.rpmTpmHelp, /last 60 seconds/i)
  assert.match(source, /aria-label=\{t\('dashboard\.rpmTpmHelpLabel'\)\}/)
  assert.match(source, /rounded-full[^"\n]*bg-amber/)
  assert.match(source, /<TooltipContent/)
})
