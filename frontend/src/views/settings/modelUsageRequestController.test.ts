import assert from 'node:assert/strict'
import test from 'node:test'
import {
  createModelUsageRequestController,
  type ModelUsageRequestSnapshot,
} from './modelUsageRequestController.ts'

function snap(modelId: string | null): ModelUsageRequestSnapshot {
  return {
    tenantId: 1,
    modelId,
    preset: '24h',
    from: '2026-08-24T00:00:00.000Z',
    to: '2026-08-25T00:00:00.000Z',
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (err: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

test('success applies when its generation is current', async () => {
  const c = createModelUsageRequestController(async () => ({ ok: true }))
  await c.request(snap(null))
  const s = c.getState()
  assert.equal(s.status, 'success')
  assert.deepEqual(s.data, { ok: true })
  assert.equal(s.error, null)
  assert.equal(s.stale, false)
})

test('old success cannot overwrite new success', async () => {
  const a = deferred<{ v: string }>()
  const b = deferred<{ v: string }>()
  const calls: ModelUsageRequestSnapshot[] = []
  const c = createModelUsageRequestController((snapshot) => {
    calls.push(snapshot)
    return calls.length === 1 ? a.promise : b.promise
  })

  const pA = c.request(snap('model-a'))
  const pB = c.request(snap('model-b'))

  b.resolve({ v: 'B' })
  await pB
  assert.deepEqual(c.getState().data, { v: 'B' })
  assert.equal(c.getState().snapshot?.modelId, 'model-b')

  // late A success must be ignored
  a.resolve({ v: 'A' })
  await pA
  assert.deepEqual(c.getState().data, { v: 'B' })
  assert.equal(c.getState().snapshot?.modelId, 'model-b')
})

test('old error cannot replace new success', async () => {
  const a = deferred<{ v: string }>()
  const b = deferred<{ v: string }>()
  const calls: ModelUsageRequestSnapshot[] = []
  const c = createModelUsageRequestController((snapshot) => {
    calls.push(snapshot)
    return calls.length === 1 ? a.promise : b.promise
  })

  const pA = c.request(snap('model-a'))
  const pB = c.request(snap('model-b'))

  b.resolve({ v: 'B' })
  await pB
  a.reject(new Error('stale boom'))
  await pA

  assert.equal(c.getState().status, 'success')
  assert.deepEqual(c.getState().data, { v: 'B' })
  assert.equal(c.getState().error, null)
})

test('new request immediately masks old data with loading', async () => {
  const a = deferred<{ v: string }>()
  const b = deferred<{ v: string }>()
  const calls: ModelUsageRequestSnapshot[] = []
  const c = createModelUsageRequestController((snapshot) => {
    calls.push(snapshot)
    return calls.length === 1 ? a.promise : b.promise
  })

  const pA = c.request(snap('model-a'))
  a.resolve({ v: 'A' })
  await pA
  assert.equal(c.getState().status, 'success')

  const pB = c.request(snap('model-b'))
  // before B resolves, old A data must already be gone
  assert.equal(c.getState().status, 'loading')
  assert.equal(c.getState().data, null)
  assert.equal(c.getState().snapshot?.modelId, 'model-b')

  b.resolve({ v: 'B' })
  await pB
  assert.deepEqual(c.getState().data, { v: 'B' })
})

test('new request error clears/marks stale values', async () => {
  const c = createModelUsageRequestController(async () => {
    throw new Error('boom')
  })
  await c.request(snap('model-a'))
  const s = c.getState()
  assert.equal(s.status, 'error')
  assert.equal(s.data, null)
  assert.equal(s.error, 'boom')
})

test('invalidate makes a later in-flight completion stale and ignored', async () => {
  const a = deferred<{ v: string }>()
  const c = createModelUsageRequestController(async () => a.promise)

  const pA = c.request(snap('model-a'))
  c.invalidate()
  a.resolve({ v: 'A' })
  await pA

  // invalidate bumped generation, so the completion was discarded as stale
  assert.equal(c.getState().status, 'loading')
  assert.equal(c.getState().stale, true)
  assert.equal(c.getState().data, null)
})

test('retry carries the latest snapshot, not a reverted one', async () => {
  let fail = true
  const c = createModelUsageRequestController(async (snapshot) => {
    if (fail) throw new Error('first fail')
    return { modelId: snapshot.modelId }
  })

  await c.request(snap('model-a'))
  assert.equal(c.getState().status, 'error')

  fail = false
  await c.request(snap('model-b'))
  assert.equal(c.getState().status, 'success')
  assert.equal(c.getState().snapshot?.modelId, 'model-b')
  assert.deepEqual(c.getState().data, { modelId: 'model-b' })
})
