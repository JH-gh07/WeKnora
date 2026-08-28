// Model Usage API consumer — Task004 (Gate G2B).
//
// Read-only consumer of the Task003 aggregate endpoints. It MUST NOT
// recompute cost, infer cache misses, or redefine the ModelCall grain; it only
// carries the backend's typed JSON contract into the frontend and leaves the
// number semantics to `views/settings/modelUsageState.ts`.

import { get } from '@/utils/request'

export type ModelOperation = 'chat' | 'embedding' | 'rerank'

export type MeasurementHealthStatus = 'COMPLETE' | 'PARTIAL' | 'UNKNOWN'

// Mirrors internal/types/embedding_cache.go EmbeddingCacheImplementationStatus.
export type EmbeddingCacheImplementationStatus = 'ENABLED' | 'DISABLED'

// Mirrors internal/types/embedding_cache.go EmbeddingCacheAggregate (json tags).
// It is additive to ModelUsageAggregate; a DISABLED capability still reports a
// real DISABLED state, never a fabricated zero hit rate.
export interface LocalEmbeddingCache {
  implementation_status: EmbeddingCacheImplementationStatus
  batch_invocation_count: number
  logical_item_count: number
  hit_count: number
  miss_count: number
  bypass_count: number
  lookup_failed_count: number
  corruption_count: number
  write_failed_count: number
  provider_bound_model_call_count: number
  attempted_count: number
  persisted_count: number
  failed_count: number
  measurement_status: MeasurementHealthStatus
}

// Mirrors internal/types/model_call.go ModelUsageAggregate exactly (json tags).
// Token/cost fields are `*int64` / `*float64` in Go with `omitempty`, so a NULL
// value is OMITTED from the JSON entirely — the field may be absent, not just
// `null`. They are therefore optional here; consumers must treat both `undefined`
// and `null` as "no value" and never coerce to 0.
export interface ModelUsageAggregate {
  logical_call_count: number
  success_count: number
  failure_count: number
  input_tokens?: number | null
  output_tokens?: number | null
  cache_read_tokens?: number | null
  cache_write_tokens?: number | null
  cache_miss_tokens?: number | null
  cache_reported_input_tokens?: number | null
  known_cost_total?: number | null
  currency?: string | null
  mixed_currency: boolean
  unknown_cost_call_count: number
  cache_eligible_count: number
  cache_reported_count: number
  cache_unsupported_count: number
  measurement_status: MeasurementHealthStatus
  metering_attempted_count: number
  metering_persisted_count: number
  metering_failed_count: number
  local_embedding_cache?: LocalEmbeddingCache | null
}

// Mirrors internal/types/model_call.go MeasurementHealth exactly.
export interface MeasurementHealth {
  tenant_id: number
  from: string
  to: string
  metering_attempted_count: number
  metering_persisted_count: number
  metering_failed_count: number
  measurement_status: MeasurementHealthStatus
}

interface ModelUsageEnvelope {
  success: boolean
  data: ModelUsageAggregate
  message?: string
}

export interface ModelUsageQuery {
  model_id?: string
  operation?: ModelOperation
  purpose?: string
  run_id?: string
  from: string
  to: string
}

// Pure query-string builder so the parameter identity can be inspected and
// asserted without touching axios.
export function buildModelUsageQuery(query: ModelUsageQuery): string {
  const params = new URLSearchParams()
  if (query.model_id) params.set('model_id', query.model_id)
  if (query.operation) params.set('operation', query.operation)
  if (query.purpose) params.set('purpose', query.purpose)
  if (query.run_id) params.set('run_id', query.run_id)
  params.set('from', query.from)
  params.set('to', query.to)
  return params.toString()
}

export function fetchModelUsage(query: ModelUsageQuery): Promise<ModelUsageAggregate> {
  const url = `/api/v1/model-usage?${buildModelUsageQuery(query)}`
  return get<ModelUsageEnvelope>(url).then((env) => {
    if (env && env.success && env.data) return env.data
    throw new Error(env?.message || 'model usage request failed')
  })
}
