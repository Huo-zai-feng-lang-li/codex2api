import assert from 'node:assert/strict'
import test from 'node:test'
import { createVisiblePollingController } from './visiblePollingController.ts'

function setup(initiallyVisible = true) {
  let visible = initiallyVisible
  let nextTimer = 0
  let runCount = 0
  const timers = new Map()
  const cleared = []
  const controller = createVisiblePollingController({
    intervalMs: 3_000,
    immediateOnVisible: true,
    isVisible: () => visible,
    run: () => { runCount += 1 },
    setTimer: (callback) => {
      nextTimer += 1
      timers.set(nextTimer, callback)
      return nextTimer
    },
    clearTimer: (timer) => {
      cleared.push(timer)
      timers.delete(timer)
    },
  })
  return {
    controller,
    timers,
    cleared,
    get runCount() { return runCount },
    setVisible(value) { visible = value },
  }
}

test('stops the active timer when the page becomes hidden', () => {
  const runtime = setup()
  assert.equal(runtime.timers.size, 1)

  runtime.setVisible(false)
  runtime.controller.handleVisibilityChange()

  assert.equal(runtime.timers.size, 0)
  assert.deepEqual(runtime.cleared, [1])
  assert.equal(runtime.runCount, 0)
})

test('refreshes immediately and schedules polling when visibility returns', () => {
  const runtime = setup(false)
  assert.equal(runtime.timers.size, 0)

  runtime.setVisible(true)
  runtime.controller.handleVisibilityChange()

  assert.equal(runtime.runCount, 1)
  assert.equal(runtime.timers.size, 1)
})

test('runs scheduled polls and clears the timer on disposal', () => {
  const runtime = setup()
  runtime.timers.get(1)()
  assert.equal(runtime.runCount, 1)

  runtime.controller.dispose()
  assert.equal(runtime.timers.size, 0)
  assert.deepEqual(runtime.cleared, [1])
})
