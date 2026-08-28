import assert from 'node:assert/strict'
import test from 'node:test'
import {
  buildUtcWindow,
  deriveModelUsageViewModel,
  validateAggregate,
} from './modelUsageState.ts'
import type { ModelUsageAggregate } from '@/api/model-usage'

function agg(partial: Partial<ModelUsageAggregate> = {}): ModelUsageAggregate {
  return {
    logical_call_count: 0,
    success_count: 0,
    failure_count: 0,
    input_tokens: null,
    output_tokens: null,
    cache_read_tokens: null,
    cache_write_tokens: null,
    cache_miss_tokens: null,
    known_cost_total: null,
    currency: null,
    mixed_currency: false,
    unknown_cost_call_count: 0,
    cache_eligible_count: 0,
    cache_reported_count: 0,
    cache_unsupported_count: 0,
    measurement_status: 'UNKNOWN',
    metering_attempted_count: 0,
    metering_persisted_count: 0,
    metering_failed_count: 0,
    ...partial,
  }
}

test('buildUtcWindow produces exact RFC3339 UTC boundaries for each preset', () => {
  const now = new Date('2026-08-25T12:00:00.000Z')
  const w24 = buildUtcWindow('24h', now)
  assert.equal(w24.from, '2026-08-24T12:00:00.000Z')
  assert.equal(w24.to, '2026-08-25T12:00:00.000Z')

  const w7 = buildUtcWindow('7d', now)
  assert.equal(w7.from, '2026-08-18T12:00:00.000Z')
  assert.equal(w7.to, '2026-08-25T12:00:00.000Z')

  const w30 = buildUtcWindow('30d', now)
  assert.equal(w30.from, '2026-07-26T12:00:00.000Z')
  assert.equal(w30.to, '2026-08-25T12:00:00.000Z')

  // from strictly before to
  assert.ok(w24.from < w24.to)
  assert.ok(w30.from < w30.to)
})

test('empty window yields UNKNOWN cache facts, empty cost, and passthrough health', () => {
  const vm = deriveModelUsageViewModel(agg({ logical_call_count: 0, measurement_status: 'UNKNOWN' }))
  assert.equal(vm.logicalCalls.value, 0)
  assert.equal(vm.logicalCalls.availability, 'AVAILABLE')
  assert.equal(vm.promptCacheCoverage.availability, 'UNKNOWN')
  assert.equal(vm.promptCacheCoverage.ratio, null)
  assert.equal(vm.cachedTokenRatio.availability, 'UNKNOWN')
  assert.equal(vm.cachedTokenRatio.value, null)
  assert.equal(vm.cost.windowEmpty, true)
  assert.equal(vm.cost.knownCostTotal, null)
  // Absent local_embedding_cache (old backend response) is UNKNOWN, never a
  // fabricated NOT_IMPLEMENTED or 0%.
  assert.equal(vm.localEmbeddingCache.availability, 'UNKNOWN')
  assert.equal(vm.localEmbeddingCache.hitRate, null)
  assert.deepEqual(vm.anomalies, [])
})

test('all-unsupported prompt cache → UNSUPPORTED, never miss/0%', () => {
  const vm = deriveModelUsageViewModel(
    agg({
      logical_call_count: 5,
      success_count: 5,
      cache_unsupported_count: 5,
      measurement_status: 'COMPLETE',
    }),
  )
  assert.equal(vm.promptCacheCoverage.availability, 'UNSUPPORTED')
  assert.equal(vm.promptCacheCoverage.ratio, null)
  assert.equal(vm.cachedTokenRatio.availability, 'UNSUPPORTED')
})

test('eligible but unreported → coverage is a real 0%, ratio is UNREPORTED', () => {
  const vm = deriveModelUsageViewModel(
    agg({
      logical_call_count: 3,
      success_count: 3,
      cache_eligible_count: 3,
      cache_reported_count: 0,
      cache_unsupported_count: 0,
      measurement_status: 'COMPLETE',
    }),
  )
  assert.equal(vm.promptCacheCoverage.availability, 'AVAILABLE')
  assert.equal(vm.promptCacheCoverage.ratio, 0)
  assert.equal(vm.promptCacheCoverage.numerator, 0)
  assert.equal(vm.promptCacheCoverage.denominator, 3)
  assert.equal(vm.cachedTokenRatio.availability, 'UNREPORTED')
  assert.equal(vm.cachedTokenRatio.value, null)
})

test('partial reporting → coverage and explicit-denominator cached ratio are available', () => {
  const vm = deriveModelUsageViewModel(
    agg({
      logical_call_count: 10,
      success_count: 10,
      cache_eligible_count: 10,
      cache_reported_count: 4,
      cache_unsupported_count: 0,
      cache_read_tokens: 250,
      cache_reported_input_tokens: 1000,
      measurement_status: 'COMPLETE',
    }),
  )
  assert.equal(vm.promptCacheCoverage.availability, 'AVAILABLE')
  assert.equal(vm.promptCacheCoverage.ratio, 0.4)
  assert.equal(vm.cachedTokenRatio.availability, 'AVAILABLE')
  assert.equal(vm.cachedTokenRatio.value, 0.25)
})

test('NULL token fields are never coerced to 0 (ratio stays UNKNOWN)', () => {
  const vm = deriveModelUsageViewModel(
    agg({
      logical_call_count: 2,
      success_count: 2,
      cache_eligible_count: 2,
      cache_reported_count: 2,
      cache_unsupported_count: 0,
      cache_read_tokens: null,
      input_tokens: null,
      measurement_status: 'COMPLETE',
    }),
  )
  assert.equal(vm.cachedTokenRatio.availability, 'UNKNOWN')
  assert.equal(vm.cachedTokenRatio.value, null)
})

test('frozen cost is UNKNOWN: no currency, no total, unknown count still surfaced', () => {
  const vm = deriveModelUsageViewModel(
    agg({
      logical_call_count: 4,
      success_count: 4,
      known_cost_total: 0.125,
      unknown_cost_call_count: 1,
      cache_unsupported_count: 4,
      measurement_status: 'COMPLETE',
    }),
  )
  assert.equal(vm.cost.availability, 'UNKNOWN')
  assert.equal(vm.cost.currency, null)
  assert.equal(vm.cost.unknownCostCallCount, 1)
  // UNKNOWN availability ⇒ the metric value MUST be NULL; the raw API value is
  // never surfaced in the display view model.
  assert.equal(vm.cost.knownCostTotal, null)
})

test('single historical currency exposes known subtotal without hiding unknown-price calls', () => {
  const vm = deriveModelUsageViewModel(
    agg({
      logical_call_count: 4,
      success_count: 4,
      known_cost_total: 0.125,
      currency: 'USD',
      mixed_currency: false,
      unknown_cost_call_count: 1,
      cache_unsupported_count: 4,
    }),
  )
  assert.equal(vm.cost.availability, 'AVAILABLE')
  assert.equal(vm.cost.knownCostTotal, 0.125)
  assert.equal(vm.cost.currency, 'USD')
  assert.equal(vm.cost.unknownCostCallCount, 1)
})

test('mixed currency fails closed even if a raw subtotal is present', () => {
  const vm = deriveModelUsageViewModel(
    agg({
      logical_call_count: 2,
      success_count: 2,
      known_cost_total: 3,
      currency: null,
      mixed_currency: true,
      cache_unsupported_count: 2,
    }),
  )
  assert.equal(vm.cost.availability, 'UNKNOWN')
  assert.equal(vm.cost.knownCostTotal, null)
  assert.equal(vm.cost.currency, null)
})

test('counts downgrade to UNKNOWN on success+failure non-conservation', () => {
  const vm = deriveModelUsageViewModel(
    agg({
      logical_call_count: 3,
      success_count: 1,
      failure_count: 1,
      cache_unsupported_count: 3,
      measurement_status: 'COMPLETE',
    }),
  )
  assert.equal(vm.logicalCalls.availability, 'UNKNOWN')
  assert.equal(vm.logicalCalls.value, null)
  assert.equal(vm.success.availability, 'UNKNOWN')
  assert.equal(vm.failure.availability, 'UNKNOWN')
  assert.ok(vm.anomalies.some((a) => a.includes('success(1) + failure(1) != logical(3)')))
})

test('omitempty absent token/cost fields are treated as UNKNOWN, never coerced to 0', () => {
  // Simulates the live Go JSON where NULL *int64/*float64 fields are omitted.
  const raw = {
    logical_call_count: 2,
    success_count: 2,
    failure_count: 0,
    unknown_cost_call_count: 2,
    cache_eligible_count: 2,
    cache_reported_count: 2,
    cache_unsupported_count: 0,
    measurement_status: 'COMPLETE',
    metering_attempted_count: 2,
    metering_persisted_count: 2,
    metering_failed_count: 0,
  } as ModelUsageAggregate
  const vm = deriveModelUsageViewModel(raw)
  assert.equal(vm.cachedTokenRatio.availability, 'UNKNOWN')
  assert.equal(vm.cachedTokenRatio.value, null)
  assert.equal(vm.cost.availability, 'UNKNOWN')
  assert.equal(vm.cost.knownCostTotal, null)
})

test('contract anomalies are detected and reported (reported > eligible)', () => {
  const anomalies = validateAggregate(
    agg({
      logical_call_count: 5,
      success_count: 5,
      cache_eligible_count: 2,
      cache_reported_count: 4,
      cache_unsupported_count: 3,
    }),
  )
  assert.ok(anomalies.some((a) => a.includes('reported(4) > eligible(2)')))
  // derivation degrades to UNKNOWN rather than silently correcting
  const vm = deriveModelUsageViewModel(
    agg({
      logical_call_count: 5,
      success_count: 5,
      cache_eligible_count: 2,
      cache_reported_count: 4,
      cache_unsupported_count: 3,
    }),
  )
  assert.equal(vm.promptCacheCoverage.availability, 'UNKNOWN')
  assert.equal(vm.cachedTokenRatio.availability, 'UNKNOWN')
})

test('success+failure mismatch and negative counts are anomalies', () => {
  const anomalies = validateAggregate(
    agg({ logical_call_count: 3, success_count: 1, failure_count: 1, cache_unsupported_count: 3 }),
  )
  assert.ok(anomalies.some((a) => a.includes('success(1) + failure(1) != logical(3)')))
})

test('cache conservation violation → coverage and ratio both fail-closed to UNKNOWN', () => {
  // logical=5 but eligible(2) + unsupported(2) = 4 ≠ 5: must NOT show AVAILABLE/50%.
  const vm = deriveModelUsageViewModel(
    agg({
      logical_call_count: 5,
      success_count: 5,
      cache_eligible_count: 2,
      cache_reported_count: 1,
      cache_unsupported_count: 2,
      measurement_status: 'COMPLETE',
    }),
  )
  assert.ok(vm.anomalies.some((a) => a.includes('eligible(2) + unsupported(2) != logical(5)')))
  assert.equal(vm.promptCacheCoverage.availability, 'UNKNOWN')
  assert.equal(vm.promptCacheCoverage.numerator, null)
  assert.equal(vm.promptCacheCoverage.denominator, null)
  assert.equal(vm.promptCacheCoverage.ratio, null)
  assert.equal(vm.cachedTokenRatio.availability, 'UNKNOWN')
  assert.equal(vm.cachedTokenRatio.value, null)
})

test('negative logical count fails-closed to UNKNOWN (not UNSUPPORTED/Available)', () => {
  const vm = deriveModelUsageViewModel(
    agg({
      logical_call_count: -1,
      success_count: 0,
      failure_count: 0,
      cache_eligible_count: 0,
      cache_reported_count: 0,
      cache_unsupported_count: 0,
      measurement_status: 'UNKNOWN',
    }),
  )
  assert.equal(vm.promptCacheCoverage.availability, 'UNKNOWN')
  assert.equal(vm.cachedTokenRatio.availability, 'UNKNOWN')
})

test('health view model passes tenant-window status and counts through unchanged', () => {
  const vm = deriveModelUsageViewModel(
    agg({
      logical_call_count: 1,
      success_count: 1,
      cache_unsupported_count: 1,
      measurement_status: 'PARTIAL',
      metering_attempted_count: 7,
      metering_persisted_count: 5,
      metering_failed_count: 2,
    }),
  )
  assert.equal(vm.health.status, 'PARTIAL')
  assert.equal(vm.health.attempted, 7)
  assert.equal(vm.health.persisted, 5)
  assert.equal(vm.health.failed, 2)
})

test('disabled local embedding cache → DISABLED, never NOT_IMPLEMENTED or 0%', () => {
  const vm = deriveModelUsageViewModel(
    agg({
      local_embedding_cache: {
        implementation_status: 'DISABLED',
        batch_invocation_count: 0,
        logical_item_count: 0,
        hit_count: 0,
        miss_count: 0,
        bypass_count: 0,
        lookup_failed_count: 0,
        corruption_count: 0,
        write_failed_count: 0,
        provider_bound_model_call_count: 0,
        attempted_count: 0,
        persisted_count: 0,
        failed_count: 0,
        measurement_status: 'UNKNOWN',
      },
    }),
  )
  assert.equal(vm.localEmbeddingCache.availability, 'DISABLED')
  assert.equal(vm.localEmbeddingCache.hitRate, null)
})

test('enabled local cache with conserving counts and COMPLETE health → AVAILABLE hit rate', () => {
  const vm = deriveModelUsageViewModel(
    agg({
      local_embedding_cache: {
        implementation_status: 'ENABLED',
        batch_invocation_count: 2,
        logical_item_count: 10,
        hit_count: 6,
        miss_count: 4,
        bypass_count: 0,
        lookup_failed_count: 0,
        corruption_count: 0,
        write_failed_count: 0,
        provider_bound_model_call_count: 2,
        attempted_count: 2,
        persisted_count: 2,
        failed_count: 0,
        measurement_status: 'COMPLETE',
      },
    }),
  )
  assert.equal(vm.localEmbeddingCache.availability, 'AVAILABLE')
  assert.equal(vm.localEmbeddingCache.hitRate, 0.6)
  assert.equal(vm.localEmbeddingCache.hitCount, 6)
  assert.equal(vm.localEmbeddingCache.logicalItemCount, 10)
})

test('enabled local cache with PARTIAL health → UNKNOWN (no benefit conclusion)', () => {
  const vm = deriveModelUsageViewModel(
    agg({
      local_embedding_cache: {
        implementation_status: 'ENABLED',
        batch_invocation_count: 1,
        logical_item_count: 5,
        hit_count: 3,
        miss_count: 1,
        bypass_count: 0,
        lookup_failed_count: 0,
        corruption_count: 0,
        write_failed_count: 0,
        provider_bound_model_call_count: 1,
        attempted_count: 1,
        persisted_count: 0,
        failed_count: 1,
        measurement_status: 'PARTIAL',
      },
    }),
  )
  assert.equal(vm.localEmbeddingCache.availability, 'UNKNOWN')
  assert.equal(vm.localEmbeddingCache.hitRate, null)
})

test('lookup/corruption/write failures are surfaced, never folded into miss or 0%', () => {
  const vm = deriveModelUsageViewModel(
    agg({
      local_embedding_cache: {
        implementation_status: 'ENABLED',
        batch_invocation_count: 1,
        logical_item_count: 8,
        hit_count: 4,
        miss_count: 1,
        bypass_count: 0,
        lookup_failed_count: 1,
        corruption_count: 1,
        write_failed_count: 1,
        provider_bound_model_call_count: 1,
        attempted_count: 1,
        persisted_count: 1,
        failed_count: 0,
        measurement_status: 'COMPLETE',
      },
    }),
  )
  // hit(4)+miss(1)+bypass(0)+lookup(1)+corruption(1) = 7 != 8 → not conserving.
  assert.equal(vm.localEmbeddingCache.availability, 'UNKNOWN')
  assert.equal(vm.localEmbeddingCache.lookupFailedCount, 1)
  assert.equal(vm.localEmbeddingCache.corruptionCount, 1)
  assert.equal(vm.localEmbeddingCache.writeFailedCount, 1)
})
