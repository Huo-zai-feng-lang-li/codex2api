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

const metricAnimation = await loadModule('./metricAnimation.ts')

test('keeps metric rolling independent of the system reduced-motion preference', () => {
  const componentSource = fs.readFileSync(
    new URL('../components/AnimatedMetricValue.tsx', import.meta.url),
    'utf8',
  )

  assert.doesNotMatch(componentSource, /prefers-reduced-motion|matchMedia/)
})

test('updates through timer-driven intermediate values before the exact target', () => {
  let now = 0
  let tick = () => {}
  const intervals = []
  const clearedTimers = []
  const values = []
  const clock = {
    now: () => now,
    setInterval: (callback, intervalMs) => {
      tick = callback
      intervals.push(intervalMs)
      return 7
    },
    clearInterval: (timerId) => clearedTimers.push(timerId),
  }

  metricAnimation.startMetricAnimation({
    from: 10,
    to: 110,
    durationMs: 1_000,
    onUpdate: (value) => values.push(value),
    clock,
  })

  assert.deepEqual(intervals, [metricAnimation.METRIC_ANIMATION_INTERVAL_MS])
  assert.deepEqual(values, [10])

  now = 500
  tick()
  assert.equal(values.at(-1), 60)

  now = 1_000
  tick()
  assert.equal(values.at(-1), 110)
  assert.deepEqual(clearedTimers, [7])
})

test('resets manual range changes to zero but keeps automatic refresh continuity', () => {
  assert.equal(metricAnimation.resolveMetricAnimationStart(undefined, false), 0)
  assert.equal(metricAnimation.resolveMetricAnimationStart(42, true), 0)
  assert.equal(metricAnimation.resolveMetricAnimationStart(42, false), 42)
})

test('reserves the widest formatted value across the full animation path', () => {
  const sizingValue = metricAnimation.resolveMetricSizingValue(
    0,
    100,
    (value) => value === 50 ? 'widest-value' : 'x',
  )

  assert.equal(sizingValue, 50)
})
