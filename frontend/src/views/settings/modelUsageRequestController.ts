// Minimal local request-generation guard for the Usage panel (Task004 Step 4).
//
// The existing VersionedRequestCoordinator only protects apply(value); it does
// not guard the component's outer stale error path nor parameter-snapshot
// changes. This controller captures the request identity at start and masks
// prior tenant/filter data immediately, then only lets a completion land if its
// generation is still current — so a slow tenant-A response can never paint
// tenant-B state.

import type { TimePreset } from './modelUsageState'

export type RequestStatus = 'idle' | 'loading' | 'success' | 'error'

export interface ModelUsageRequestSnapshot {
  tenantId: number | null
  modelId: string | null
  preset: TimePreset
  from: string
  to: string
}

export interface ModelUsageRequestState<T> {
  status: RequestStatus
  data: T | null
  error: string | null
  snapshot: ModelUsageRequestSnapshot | null
  generation: number
  stale: boolean
}

export interface ModelUsageRequestController<T> {
  request: (snapshot: ModelUsageRequestSnapshot) => Promise<void>
  invalidate: () => void
  getState: () => ModelUsageRequestState<T>
}

function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message
  return String(err)
}

export function createModelUsageRequestController<T>(
  fetcher: (snapshot: ModelUsageRequestSnapshot) => Promise<T>,
): ModelUsageRequestController<T> {
  let generation = 0
  let state: ModelUsageRequestState<T> = {
    status: 'idle',
    data: null,
    error: null,
    snapshot: null,
    generation: 0,
    stale: false,
  }

  const getState = (): ModelUsageRequestState<T> => state

  async function request(snapshot: ModelUsageRequestSnapshot): Promise<void> {
    const gen = ++generation
    // Immediately clear prior tenant/filter data; loading carries the new
    // snapshot identity so the UI can show "loading (tenant X / 30d)".
    state = { status: 'loading', data: null, error: null, snapshot, generation: gen, stale: false }
    try {
      const value = await fetcher(snapshot)
      if (gen !== generation) return // stale success — do not overwrite
      state = { status: 'success', data: value, error: null, snapshot, generation: gen, stale: false }
    } catch (err) {
      if (gen !== generation) return // stale error — do not replace newer state
      state = {
        status: 'error',
        data: null,
        error: errorMessage(err),
        snapshot,
        generation: gen,
        stale: false,
      }
    }
  }

  function invalidate(): void {
    generation += 1
    state = { ...state, stale: true, generation }
  }

  return { request, invalidate, getState }
}
