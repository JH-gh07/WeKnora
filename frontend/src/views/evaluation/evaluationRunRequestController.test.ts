import assert from 'node:assert/strict'
import test from 'node:test'

import { createEvaluationRunRequestController } from './evaluationRunRequestController.ts'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (err: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

test('a slow run-A success never overwrites run B', async () => {
  const pending = new Map<string, ReturnType<typeof deferred<string>>>()
  const controller = createEvaluationRunRequestController((runId: string) => {
    const d = deferred<string>()
    pending.set(runId, d)
    return d.promise
  })

  const a = controller.request('run-A')
  const b = controller.request('run-B')
  assert.equal(controller.getState().runId, 'run-B')

  // B finishes first, then A's late response arrives.
  pending.get('run-B')!.resolve('report-B')
  await b
  assert.equal(controller.getState().data, 'report-B')
  assert.equal(controller.getState().runId, 'run-B')

  pending.get('run-A')!.resolve('report-A-stale')
  await a
  // A's stale success must NOT overwrite B.
  assert.equal(controller.getState().data, 'report-B')
  assert.equal(controller.getState().runId, 'run-B')
  assert.equal(controller.getState().status, 'success')
})

test('a slow run-A error never overwrites run B', async () => {
  const pending = new Map<string, ReturnType<typeof deferred<string>>>()
  const controller = createEvaluationRunRequestController((runId: string) => {
    const d = deferred<string>()
    pending.set(runId, d)
    return d.promise
  })

  const a = controller.request('run-A')
  const b = controller.request('run-B')
  pending.get('run-B')!.resolve('report-B')
  await b

  pending.get('run-A')!.reject(new Error('run-A failed late'))
  await a
  assert.equal(controller.getState().data, 'report-B')
  assert.equal(controller.getState().status, 'success')
  assert.equal(controller.getState().error, null)
})

test('invalidate marks the current state stale', async () => {
  const controller = createEvaluationRunRequestController(async () => 'report')
  await controller.request('run-A')
  controller.invalidate()
  assert.equal(controller.getState().stale, true)
  assert.equal(controller.getState().data, 'report')
})

test('an Error is normalized to message only', async () => {
  const controller = createEvaluationRunRequestController(async () => {
    throw new Error('boom')
  })
  await controller.request('run-A')
  assert.equal(controller.getState().status, 'error')
  assert.deepEqual(controller.getState().error, { message: 'boom' })
})

test('a transport error object keeps its HTTP status', async () => {
  const controller = createEvaluationRunRequestController(async () => {
    throw { status: 404, message: 'not found' }
  })
  await controller.request('run-A')
  assert.deepEqual(controller.getState().error, { status: 404, message: 'not found' })
})

test('the state reference observed by a Vue consumer remains live', async () => {
  const controller = createEvaluationRunRequestController(async () => 'report')
  const observed = controller.state

  await controller.request('run-A')

  assert.equal(observed.value.status, 'success')
  assert.equal(observed.value.data, 'report')
  assert.equal(observed.value.runId, 'run-A')
})
