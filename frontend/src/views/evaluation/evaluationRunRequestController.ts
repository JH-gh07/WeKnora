// Reactive request-generation guard for the Evaluation Run Detail page
// (Task010 Step 4, matrix B14).
//
// A slow response for run A must never paint over run B after the route param
// changes. Each `request(runId)` bumps the generation and clears prior data
// immediately; a completion only lands if its generation is still current.
// This is the same contract as Task004's model-usage controller, kept local
// so the two pages cannot drift.

import { shallowRef, type ShallowRef } from 'vue'

export type RequestStatus = 'idle' | 'loading' | 'success' | 'error'

// Normalized request error: carries the HTTP status (404/403/500/...) when the
// transport exposes it, plus a human message. The page uses `status` to pick a
// distinct not-found vs forbidden vs server-error presentation.
export interface RequestError {
  status?: number
  message: string
}

export interface EvaluationRunRequestState<T> {
  status: RequestStatus
  data: T | null
  error: RequestError | null
  runId: string | null
  generation: number
  stale: boolean
}

export interface EvaluationRunRequestController<T> {
  request: (runId: string) => Promise<void>
  invalidate: () => void
  getState: () => EvaluationRunRequestState<T>
  state: Readonly<ShallowRef<EvaluationRunRequestState<T>>>
}

function normalizeError(err: unknown): RequestError {
  if (err instanceof Error) return { message: err.message }
  if (typeof err === 'object' && err !== null) {
    const anyErr = err as { status?: unknown; message?: unknown }
    const status = typeof anyErr.status === 'number' ? anyErr.status : undefined
    const message = typeof anyErr.message === 'string' ? anyErr.message : String(err)
    return { status, message }
  }
  return { message: String(err) }
}

export function createEvaluationRunRequestController<T>(
  fetcher: (runId: string) => Promise<T>,
): EvaluationRunRequestController<T> {
  let generation = 0
  const state = shallowRef<EvaluationRunRequestState<T>>({
    status: 'idle',
    data: null,
    error: null,
    runId: null,
    generation: 0,
    stale: false,
  })

  const getState = (): EvaluationRunRequestState<T> => state.value

  async function request(runId: string): Promise<void> {
    const gen = ++generation
    // Immediately clear prior run's data so a stale success cannot flash the
    // old run's report while the new one loads.
    state.value = { status: 'loading', data: null, error: null, runId, generation: gen, stale: false }
    try {
      const value = await fetcher(runId)
      if (gen !== generation) return // stale success — do not overwrite
      state.value = { status: 'success', data: value, error: null, runId, generation: gen, stale: false }
    } catch (err) {
      if (gen !== generation) return // stale error — do not replace newer state
      state.value = {
        status: 'error',
        data: null,
        error: normalizeError(err),
        runId,
        generation: gen,
        stale: false,
      }
    }
  }

  function invalidate(): void {
    generation += 1
    state.value = { ...state.value, stale: true, generation }
  }

  return { request, invalidate, getState, state }
}
