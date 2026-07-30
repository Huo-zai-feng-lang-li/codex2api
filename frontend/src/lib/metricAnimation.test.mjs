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

test('updates through animation frames before the exact target', () => {
  let now = 0
  const frames = []
  const cancelledFrames = []
  const values = []
  const clock = {
    now: () => now,
    requestFrame: (callback) => {
      frames.push(callback)
      return frames.length
    },
    cancelFrame: (frameId) => cancelledFrames.push(frameId),
  }

  metricAnimation.startMetricAnimation({
    from: 10,
    to: 110,
    durationMs: 1_000,
    onUpdate: (value) => values.push(value),
    clock,
  })

  assert.deepEqual(values, [10])
  assert.equal(frames.length, 1)

  now = 500
  frames.shift()()
  assert.equal(values.at(-1), 60)
  assert.equal(frames.length, 1)

  now = 1_000
  frames.shift()()
  assert.equal(values.at(-1), 110)
  assert.equal(frames.length, 0)
  assert.deepEqual(cancelledFrames, [])
})

test('animates downward and lands on the exact target', () => {
  let now = 0
  const frames = []
  const values = []
  const clock = {
    now: () => now,
    requestFrame: (callback) => { frames.push(callback); return frames.length },
    cancelFrame: () => {},
  }

  metricAnimation.startMetricAnimation({
    from: 110,
    to: 10,
    durationMs: 1_000,
    onUpdate: (value) => values.push(value),
    clock,
  })
  now = 1_000
  frames.shift()()

  assert.deepEqual(values, [110, 10])
})

test('does not schedule a frame when the value is unchanged', () => {
  let scheduled = false
  const values = []
  metricAnimation.startMetricAnimation({
    from: 42,
    to: 42,
    durationMs: 1_000,
    onUpdate: (value) => values.push(value),
    clock: {
      now: () => 0,
      requestFrame: () => { scheduled = true; return 1 },
      cancelFrame: () => {},
    },
  })

  assert.equal(scheduled, false)
  assert.deepEqual(values, [42])
})

test('cancels the pending frame without emitting stale values', () => {
  let frame = () => {}
  const cancelledFrames = []
  const values = []
  const stop = metricAnimation.startMetricAnimation({
    from: 10,
    to: 20,
    durationMs: 1_000,
    onUpdate: (value) => values.push(value),
    clock: {
      now: () => 0,
      requestFrame: (callback) => { frame = callback; return 7 },
      cancelFrame: (frameId) => cancelledFrames.push(frameId),
    },
  })

  stop()
  frame()

  assert.deepEqual(cancelledFrames, [7])
  assert.deepEqual(values, [10])
})

test('quantizes display updates and skips duplicate visual steps', () => {
  let now = 0
  const frames = []
  const values = []
  const clock = {
    now: () => now,
    requestFrame: (callback) => { frames.push(callback); return frames.length },
    cancelFrame: () => {},
  }

  metricAnimation.startMetricAnimation({
    from: 0,
    to: 95,
    durationMs: 1_000,
    step: 10,
    onUpdate: (value) => values.push(value),
    clock,
  })

  now = 250
  frames.shift()()
  now = 260
  frames.shift()()
  now = 1_000
  frames.shift()()

  assert.deepEqual(values, [0, 20, 95])
})

test('chooses animation steps from the displayed token and money precision', () => {
  assert.equal(metricAnimation.resolveTokenAnimationStep(999, 'en-US'), 1)
  assert.equal(metricAnimation.resolveTokenAnimationStep(12_345, 'en-US'), 100)
  assert.equal(metricAnimation.resolveTokenAnimationStep(1_234_567, 'en-US'), 100_000)
  assert.equal(metricAnimation.resolveTokenAnimationStep(12_345, 'zh-CN'), 100)
  assert.equal(metricAnimation.resolveTokenAnimationStep(123_456, 'zh-CN'), 1_000)
  assert.equal(metricAnimation.resolveMoneyAnimationStep(0.5), 0.0001)
  assert.equal(metricAnimation.resolveMoneyAnimationStep(12.34), 0.01)
  assert.equal(metricAnimation.resolveMoneyAnimationStep(123.4), 0.1)
})

test('keeps automatic refresh continuity', () => {
  assert.equal(metricAnimation.resolveMetricAnimationStart(undefined, false), 0)
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

test('reserves width across a compact-format suffix boundary', () => {
  const format = (value) => value !== undefined && value >= 1_000 ? '1.0K' : String(Math.round(value ?? 0))
  const sizingValue = metricAnimation.resolveMetricSizingValue(
    999,
    1_001,
    format,
  )

  assert.equal(format(sizingValue), '1.0K')
})
