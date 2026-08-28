// Pure, framework-free derivation of the Model Usage view model (Task004).
//
// All of the "how honest is this number" logic lives here, in one place, so it
// can be driven by a shared fixture and unit-tested exhaustively. The UI only
// formats/renders the derived AvailabilityState; it never recomputes backend
// facts, never coerces NULL to 0, and never re-derives cost or cache misses.

import type { MeasurementHealthStatus, ModelUsageAggregate } from '@/api/model-usage'

// Frozen AvailabilityState enum (§4.3). `DISABLED` was added by Change Review
// 005-1 (Task005): a capability that exists but has its rollout switch off is
// a distinct fact from NOT_IMPLEMENTED, UNKNOWN, or a real 0%.
export type AvailabilityState =
  | 'AVAILABLE'
  | 'NOT_IMPLEMENTED'
  | 'UNSUPPORTED'
  | 'UNREPORTED'
  | 'UNKNOWN'
  | 'DISABLED'

export type TimePreset = '24h' | '7d' | '30d'

export interface UtcWindow {
  preset: TimePreset
  from: string // RFC3339 UTC
  to: string // RFC3339 UTC
}

export interface NumericFact {
  availability: AvailabilityState
  value: number | null
}

export interface CoverageFact {
  availability: AvailabilityState
  numerator: number | null
  denominator: number | null
  ratio: number | null // [0,1] when AVAILABLE, otherwise null
}

export interface CostFact {
  windowEmpty: boolean
  availability: AvailabilityState
  knownCostTotal: number | null
  currency: string | null
  unknownCostCallCount: number
}

export interface HealthFact {
  status: MeasurementHealthStatus
  attempted: number
  persisted: number
  failed: number
}

// Local embedding cache fact. hitRate is [0,1] when AVAILABLE, otherwise null.
// Failure counts are surfaced independently and NEVER folded into a miss or a
// fake 0%.
export interface LocalEmbeddingCacheFact {
  availability: AvailabilityState
  hitRate: number | null
  hitCount: number
  missCount: number
  logicalItemCount: number
  lookupFailedCount: number
  corruptionCount: number
  writeFailedCount: number
  providerBoundModelCallCount: number
  measurementStatus: MeasurementHealthStatus | null
}

export interface ModelUsageViewModel {
  logicalCalls: NumericFact
  success: NumericFact
  failure: NumericFact
  promptCacheCoverage: CoverageFact
  cachedTokenRatio: NumericFact
  localEmbeddingCache: LocalEmbeddingCacheFact
  cost: CostFact
  health: HealthFact
  anomalies: string[]
}

const PRESET_HOURS: Record<TimePreset, number> = { '24h': 24, '7d': 168, '30d': 720 }

// Deterministic UTC window builder. `to` is inclusive-exclusive on the backend
// (created_at < to). We pass RFC3339 with the same clock instant the caller
// observed, so the displayed boundary and the request boundary are identical.
export function buildUtcWindow(preset: TimePreset, now: Date = new Date()): UtcWindow {
  const to = new Date(now.getTime())
  const from = new Date(to.getTime() - PRESET_HOURS[preset] * 3_600_000)
  return { preset, from: from.toISOString(), to: to.toISOString() }
}

// Contract anomaly checks (reported>eligible, negative counts, success+failure
// mismatch, calls present with neither known nor unknown pricing, etc.). The
// derivation treats any anomaly as UNKNOWN; this list lets the UI record/log
// the specific violation instead of silently correcting it (§4.3, §4.2).
export function validateAggregate(agg: ModelUsageAggregate): string[] {
  const anomalies: string[] = []
  if (agg.logical_call_count < 0 || agg.success_count < 0 || agg.failure_count < 0) {
    anomalies.push('negative call count')
  }
  if (agg.success_count + agg.failure_count !== agg.logical_call_count) {
    anomalies.push(
      `success(${agg.success_count}) + failure(${agg.failure_count}) != logical(${agg.logical_call_count})`,
    )
  }
  if (
    agg.cache_eligible_count < 0 ||
    agg.cache_reported_count < 0 ||
    agg.cache_unsupported_count < 0
  ) {
    anomalies.push('negative prompt-cache count')
  }
  if (agg.cache_reported_count > agg.cache_eligible_count) {
    anomalies.push(
      `reported(${agg.cache_reported_count}) > eligible(${agg.cache_eligible_count})`,
    )
  }
  if (agg.cache_eligible_count + agg.cache_unsupported_count !== agg.logical_call_count) {
    anomalies.push(
      `eligible(${agg.cache_eligible_count}) + unsupported(${agg.cache_unsupported_count}) != logical(${agg.logical_call_count})`,
    )
  }
  if (agg.unknown_cost_call_count < 0) {
    anomalies.push('negative unknown-cost count')
  }
  if (agg.cache_reported_input_tokens != null && agg.cache_reported_input_tokens < 0) {
    anomalies.push('negative reported-input denominator')
  }
  if (
    agg.cache_read_tokens != null &&
    agg.cache_reported_input_tokens != null &&
    agg.cache_read_tokens > agg.cache_reported_input_tokens
  ) {
    anomalies.push('cached input exceeds reported-input denominator')
  }
  if (
    agg.logical_call_count > 0 &&
    agg.known_cost_total == null &&
    agg.unknown_cost_call_count === 0
  ) {
    anomalies.push('calls present but neither known nor unknown pricing')
  }
  if (
    agg.metering_attempted_count < 0 ||
    agg.metering_persisted_count < 0 ||
    agg.metering_failed_count < 0
  ) {
    anomalies.push('negative metering count')
  }
  return anomalies
}

export function deriveCounts(agg: ModelUsageAggregate): {
  logicalCalls: NumericFact
  success: NumericFact
  failure: NumericFact
} {
  // A contract anomaly (negative counts, or success+failure not conserving to
  // logical calls) must NOT be presented as AVAILABLE. Downgrade to UNKNOWN/NULL
  // rather than showing internally inconsistent numbers as if they were facts.
  const consistent =
    agg.logical_call_count >= 0 &&
    agg.success_count >= 0 &&
    agg.failure_count >= 0 &&
    agg.success_count + agg.failure_count === agg.logical_call_count
  const availability: AvailabilityState = consistent ? 'AVAILABLE' : 'UNKNOWN'
  return {
    logicalCalls: { availability, value: consistent ? agg.logical_call_count : null },
    success: { availability, value: consistent ? agg.success_count : null },
    failure: { availability, value: consistent ? agg.failure_count : null },
  }
}

// Single source of truth for prompt-cache count conservation. Both coverage and
// cached-token ratio must fail-closed on the same anomalies so the two derived
// facts can never drift apart. Returns false on any contract violation:
// negative counts, reported > eligible, or eligible+unsupported != logical.
function cacheCountsConsistent(agg: ModelUsageAggregate): boolean {
  return (
    agg.logical_call_count >= 0 &&
    agg.cache_eligible_count >= 0 &&
    agg.cache_reported_count >= 0 &&
    agg.cache_unsupported_count >= 0 &&
    agg.cache_eligible_count + agg.cache_unsupported_count === agg.logical_call_count &&
    agg.cache_reported_count <= agg.cache_eligible_count
  )
}

// Prompt-cache reporting coverage (§4.3). Count-level facts are only shown when
// the cache counts conserve; any contract anomaly yields UNKNOWN/NULL.
export function derivePromptCacheCoverage(agg: ModelUsageAggregate): CoverageFact {
  const logical = agg.logical_call_count
  const eligible = agg.cache_eligible_count
  const reported = agg.cache_reported_count
  const unsupported = agg.cache_unsupported_count
  const unknown: CoverageFact = {
    availability: 'UNKNOWN',
    numerator: null,
    denominator: null,
    ratio: null,
  }
  if (logical === 0) return unknown
  if (!cacheCountsConsistent(agg)) return unknown
  if (eligible === 0 && unsupported === logical) {
    return { availability: 'UNSUPPORTED', numerator: null, denominator: null, ratio: null }
  }
  if (eligible === 0) return unknown // mixed/unclassified, do not guess
  if (reported === 0) {
    return { availability: 'AVAILABLE', numerator: 0, denominator: eligible, ratio: 0 }
  }
  return {
    availability: 'AVAILABLE',
    numerator: reported,
    denominator: eligible,
    ratio: reported / eligible,
  }
}

// Cached-token ratio (§4.3). The denominator is an explicit additive backend
// contract. Missing/invalid values fail closed; read/(read+miss) and aggregate
// input_tokens are never used as substitutes.
export function deriveCachedTokenRatio(agg: ModelUsageAggregate): NumericFact {
  const logical = agg.logical_call_count
  const eligible = agg.cache_eligible_count
  const reported = agg.cache_reported_count
  const unsupported = agg.cache_unsupported_count
  const unknown: NumericFact = { availability: 'UNKNOWN', value: null }
  if (logical === 0) return unknown
  if (!cacheCountsConsistent(agg)) return unknown
  if (eligible === 0 && unsupported === logical) return { availability: 'UNSUPPORTED', value: null }
  if (eligible === 0) return unknown
  if (reported === 0) return { availability: 'UNREPORTED', value: null }
  const denominator = agg.cache_reported_input_tokens
  const cached = agg.cache_read_tokens
  if (denominator == null || cached == null || denominator <= 0 || cached < 0 || cached > denominator) {
    return unknown
  }
  return { availability: 'AVAILABLE', value: cached / denominator }
}

// Cost derivation (§4.3). A known subtotal is displayable only with the
// repository-proven single historical currency. Old responses, mixed currency,
// or missing identity remain UNKNOWN; unknown-price calls stay a separate fact.
export function deriveCost(agg: ModelUsageAggregate): CostFact {
  const logical = agg.logical_call_count
  const unknown = agg.unknown_cost_call_count
  if (logical === 0) {
    return {
      windowEmpty: true,
      availability: 'UNKNOWN',
      knownCostTotal: null,
      currency: null,
      unknownCostCallCount: 0,
    }
  }
  const total = agg.known_cost_total
  const currency = agg.currency?.trim() || null
  const available = total != null && total >= 0 && currency != null && !agg.mixed_currency
  return {
    windowEmpty: false,
    availability: available ? 'AVAILABLE' : 'UNKNOWN',
    knownCostTotal: available ? total : null,
    currency: available ? currency : null,
    unknownCostCallCount: unknown,
  }
}

export function deriveHealth(agg: ModelUsageAggregate): HealthFact {
  return {
    status: agg.measurement_status,
    attempted: agg.metering_attempted_count,
    persisted: agg.metering_persisted_count,
    failed: agg.metering_failed_count,
  }
}

// Local embedding cache derivation (§4.4). hit_rate = hit_count /
// logical_item_count is AVAILABLE only when the capability is ENABLED, the
// window has items, measurement is COMPLETE, and the item counts conserve
// (hit+miss+bypass+lookup_failed+corruption == logical_item_count). Failure
// counts never become miss or 0%.
export function deriveLocalEmbeddingCache(agg: ModelUsageAggregate): LocalEmbeddingCacheFact {
  const local = agg.local_embedding_cache
  const unknown: LocalEmbeddingCacheFact = {
    availability: 'UNKNOWN',
    hitRate: null,
    hitCount: 0,
    missCount: 0,
    logicalItemCount: 0,
    lookupFailedCount: 0,
    corruptionCount: 0,
    writeFailedCount: 0,
    providerBoundModelCallCount: 0,
    measurementStatus: null,
  }
  if (!local) return unknown
  if (local.implementation_status === 'DISABLED') {
    return {
      availability: 'DISABLED',
      hitRate: null,
      hitCount: 0,
      missCount: 0,
      logicalItemCount: 0,
      lookupFailedCount: 0,
      corruptionCount: 0,
      writeFailedCount: 0,
      providerBoundModelCallCount: 0,
      measurementStatus: local.measurement_status,
    }
  }
  const counts = [
    local.hit_count,
    local.miss_count,
    local.bypass_count,
    local.lookup_failed_count,
    local.corruption_count,
  ]
  const conserving =
    local.logical_item_count >= 0 &&
    counts.every((n) => n >= 0) &&
    counts.reduce((a, b) => a + b, 0) === local.logical_item_count
  const available =
    local.implementation_status === 'ENABLED' &&
    local.logical_item_count > 0 &&
    local.measurement_status === 'COMPLETE' &&
    conserving
  return {
    availability: available ? 'AVAILABLE' : 'UNKNOWN',
    hitRate: available ? local.hit_count / local.logical_item_count : null,
    hitCount: local.hit_count,
    missCount: local.miss_count,
    logicalItemCount: local.logical_item_count,
    lookupFailedCount: local.lookup_failed_count,
    corruptionCount: local.corruption_count,
    writeFailedCount: local.write_failed_count,
    providerBoundModelCallCount: local.provider_bound_model_call_count,
    measurementStatus: local.measurement_status,
  }
}

export function deriveModelUsageViewModel(agg: ModelUsageAggregate): ModelUsageViewModel {
  return {
    ...deriveCounts(agg),
    promptCacheCoverage: derivePromptCacheCoverage(agg),
    cachedTokenRatio: deriveCachedTokenRatio(agg),
    localEmbeddingCache: deriveLocalEmbeddingCache(agg),
    cost: deriveCost(agg),
    health: deriveHealth(agg),
    anomalies: validateAggregate(agg),
  }
}
