<template>
  <div class="evaluation-run-detail" data-testid="evaluation-run-detail">
    <!-- Header: short run id + lifecycle status (text, not color-only) -->
    <div class="section-header">
      <div class="section-header__top">
        <div>
          <h2>{{ $t('evaluationRun.detail.title', { id: shortId }) }}</h2>
          <p class="section-description">{{ $t('evaluationRun.detail.description') }}</p>
        </div>
        <t-button
          data-testid="refresh-report"
          theme="default"
          variant="outline"
          size="medium"
          :loading="state.status === 'loading'"
          @click="refresh"
        >
          <template #icon><refresh-icon /></template>
          {{ $t('evaluationRun.detail.refresh') }}
        </t-button>
      </div>
    </div>

    <!-- Loading -->
    <div v-if="state.status === 'loading'" class="state-block" data-testid="report-loading" role="status" aria-live="polite">
      <t-loading size="small" />
      <span>{{ $t('evaluationRun.detail.loading') }}</span>
    </div>

    <!-- Error: not found / forbidden / generic server error are distinct -->
    <div v-else-if="state.status === 'error'" class="state-block" data-testid="report-error" role="alert">
      <t-alert theme="error" :message="errorTitle">
        <template #operation>
          <t-button size="small" @click="refresh">{{ $t('evaluationRun.detail.retry') }}</t-button>
        </template>
      </t-alert>
      <p v-if="state.error?.message" class="error-detail">{{ state.error.message }}</p>
    </div>

    <!-- Report -->
    <div v-else-if="report" class="report-body" data-testid="report-body">
      <t-alert
        v-if="!isTerminal"
        theme="warning"
        :message="pollStopped
          ? $t('evaluationRun.detail.refreshStopped')
          : $t('evaluationRun.detail.runningNote')"
        class="run-banner"
      />

      <!-- Run identity / lifecycle / provenance -->
      <t-card class="run-card" data-testid="run-metadata" :title="$t('evaluationRun.detail.runMetaTitle')" :bordered="true">
        <div class="kv-grid">
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.runId') }}</span><span class="kv-value">{{ report.run.run_id }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.legacyTaskId') }}</span><span class="kv-value">{{ report.run.legacy_task_id || fallback }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.status') }}</span><span class="kv-value"><t-tag data-testid="run-status" :theme="statusTone" variant="light">{{ runStatusLabel }}</t-tag></span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.protocolHash') }}</span><span class="kv-value mono">{{ report.run.protocol_hash || fallback }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.gitCommit') }}</span><span class="kv-value mono">{{ report.run.git_commit || fallback }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.appVersion') }}</span><span class="kv-value">{{ report.run.app_version || fallback }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.startedAt') }}</span><span class="kv-value">{{ report.run.started_at || fallback }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.endedAt') }}</span><span class="kv-value">{{ report.run.ended_at || fallback }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.persistenceStatus') }}</span><span class="kv-value">{{ report.run.persistence_status || fallback }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cleanupStatus') }}</span><span class="kv-value">{{ report.run.cleanup_status || fallback }}</span></div>
          <div v-if="report.run.interruption_reason" class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.interruptionReason') }}</span><span class="kv-value">{{ report.run.interruption_reason }}</span></div>
        </div>
      </t-card>

      <!-- Retrieval Quality -->
      <t-card class="run-card" data-testid="retrieval-quality" :bordered="true">
        <template #title>
          <div class="card-title-row">
            <span>{{ $t('evaluationRun.detail.sections.retrieval') }}</span>
            <t-tag :theme="availabilityToneOf(report.quality.availability)" variant="light">{{ availabilityLabelOf(report.quality.availability) }}</t-tag>
          </div>
        </template>
        <p v-if="report.quality.reason_code" class="reason-line">
          {{ $t('evaluationRun.detail.reasonCode') }}: <code>{{ report.quality.reason_code }}</code>
        </p>
        <div v-if="report.quality.retrieval" class="metric-grid">
          <div v-for="m in retrievalMetrics" :key="m.key" class="metric">
            <span class="metric-label">{{ $t(m.labelKey) }}</span>
            <span class="metric-value">{{ m.value }}</span>
          </div>
        </div>
        <p v-else class="empty-line">{{ $t('evaluationRun.detail.notAvailable') }}</p>
      </t-card>

      <!-- Answer Quality -->
      <t-card class="run-card" data-testid="answer-quality" :bordered="true">
        <template #title>
          <div class="card-title-row">
            <span>{{ $t('evaluationRun.detail.sections.answer') }}</span>
            <t-tag :theme="availabilityToneOf(report.quality.availability)" variant="light">{{ availabilityLabelOf(report.quality.availability) }}</t-tag>
          </div>
        </template>
        <p v-if="report.quality.reason_code" class="reason-line">
          {{ $t('evaluationRun.detail.reasonCode') }}: <code>{{ report.quality.reason_code }}</code>
        </p>
        <div v-if="report.quality.answer" class="metric-grid">
          <div v-for="m in answerMetrics" :key="m.key" class="metric">
            <span class="metric-label">{{ $t(m.labelKey) }}</span>
            <span class="metric-value">{{ m.value }}</span>
          </div>
        </div>
        <p v-else class="empty-line">{{ $t('evaluationRun.detail.notAvailable') }}</p>
      </t-card>

      <!-- Cost -->
      <t-card class="run-card" data-testid="cost-summary" :bordered="true">
        <template #title>
          <div class="card-title-row">
            <span>{{ $t('evaluationRun.detail.sections.cost') }}</span>
            <t-tag :theme="availabilityToneOf(report.cost.availability)" variant="light">{{ availabilityLabelOf(report.cost.availability) }}</t-tag>
          </div>
        </template>
        <p v-if="report.cost.reason_code" class="reason-line">
          {{ $t('evaluationRun.detail.reasonCode') }}: <code>{{ report.cost.reason_code }}</code>
        </p>
        <div class="kv-grid">
          <div class="kv">
            <span class="kv-label">{{ $t('evaluationRun.detail.cost.knownSubtotal') }}</span>
            <span class="kv-value">
              {{ costTotalText }}
              <t-tag v-if="report.cost.is_estimate" size="small" variant="outline" theme="warning">{{ $t('evaluationRun.detail.cost.estimate') }}</t-tag>
            </span>
          </div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cost.currency') }}</span><span class="kv-value">{{ report.cost.currency || fallback }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cost.unknownCalls') }}</span><span class="kv-value">{{ report.cost.unknown_cost_call_count }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cost.mixedCurrency') }}</span><span class="kv-value">{{ report.cost.mixed_currency ? yesLabel : noLabel }}</span></div>
        </div>
      </t-card>

      <!-- Latency -->
      <t-card class="run-card" data-testid="latency-summary" :bordered="true">
        <template #title>
          <div class="card-title-row">
            <span>{{ $t('evaluationRun.detail.sections.latency') }}</span>
            <t-tag :theme="availabilityToneOf(report.latency.availability)" variant="light">{{ availabilityLabelOf(report.latency.availability) }}</t-tag>
          </div>
        </template>
        <p v-if="report.latency.reason_code" class="reason-line">
          {{ $t('evaluationRun.detail.reasonCode') }}: <code>{{ report.latency.reason_code }}</code>
        </p>
        <div class="kv-grid">
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.latency.wallClock') }}</span><span class="kv-value">{{ wallClockText }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.latency.definition') }}</span><span class="kv-value">{{ report.latency.definition }}</span></div>
        </div>
      </t-card>

      <!-- Usage -->
      <t-card class="run-card" data-testid="usage-summary" :bordered="true">
        <template #title>
          <div class="card-title-row">
            <span>{{ $t('evaluationRun.detail.sections.usage') }}</span>
            <t-tag :theme="availabilityToneOf(report.usage.availability)" variant="light">{{ availabilityLabelOf(report.usage.availability) }}</t-tag>
          </div>
        </template>
        <p v-if="report.usage.reason_code" class="reason-line">
          {{ $t('evaluationRun.detail.reasonCode') }}: <code>{{ report.usage.reason_code }}</code>
        </p>
        <div class="kv-grid">
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.usage.logicalCalls') }}</span><span class="kv-value">{{ report.usage.logical_call_count }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.usage.success') }}</span><span class="kv-value">{{ report.usage.success_count }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.usage.failure') }}</span><span class="kv-value">{{ report.usage.failure_count }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.usage.inputTokens') }}</span><span class="kv-value">{{ tokenText(report.usage.input_tokens) }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.usage.outputTokens') }}</span><span class="kv-value">{{ tokenText(report.usage.output_tokens) }}</span></div>
        </div>
      </t-card>

      <!-- Caches (local embedding + provider prompt cache, independent) -->
      <t-card class="run-card" data-testid="cache-summary" :bordered="true" :title="$t('evaluationRun.detail.sections.cache')">
        <h4 class="subtitle">{{ $t('evaluationRun.detail.cache.localEmbedding') }}</h4>
        <div v-if="report.supporting_observation.local_embedding_cache" class="kv-grid">
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cache.implementationStatus') }}</span><span class="kv-value">{{ report.supporting_observation.local_embedding_cache.implementation_status }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cache.logicalItemCount') }}</span><span class="kv-value">{{ report.supporting_observation.local_embedding_cache.logical_item_count }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cache.hitCount') }}</span><span class="kv-value">{{ report.supporting_observation.local_embedding_cache.hit_count }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cache.missCount') }}</span><span class="kv-value">{{ report.supporting_observation.local_embedding_cache.miss_count }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cache.measurementStatus') }}</span><span class="kv-value">{{ report.supporting_observation.local_embedding_cache.measurement_status }}</span></div>
        </div>
        <p v-else class="empty-line">{{ $t('evaluationRun.detail.notAvailable') }}</p>

        <h4 class="subtitle">{{ $t('evaluationRun.detail.cache.promptCache') }}</h4>
        <div v-if="report.supporting_observation.prompt_cache" class="kv-grid">
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cache.eligible') }}</span><span class="kv-value">{{ report.supporting_observation.prompt_cache.eligible_count }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cache.reported') }}</span><span class="kv-value">{{ report.supporting_observation.prompt_cache.reported_count }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cache.unsupported') }}</span><span class="kv-value">{{ report.supporting_observation.prompt_cache.unsupported_count }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cache.cacheReadTokens') }}</span><span class="kv-value">{{ tokenText(report.supporting_observation.prompt_cache.cache_read_tokens) }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cache.cacheWriteTokens') }}</span><span class="kv-value">{{ tokenText(report.supporting_observation.prompt_cache.cache_write_tokens) }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cache.cacheMissTokens') }}</span><span class="kv-value">{{ tokenText(report.supporting_observation.prompt_cache.cache_miss_tokens) }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.cache.cacheReportedInputTokens') }}</span><span class="kv-value">{{ tokenText(report.supporting_observation.prompt_cache.cache_reported_input_tokens) }}</span></div>
        </div>
        <p v-else class="empty-line">{{ $t('evaluationRun.detail.notAvailable') }}</p>
      </t-card>

      <!-- Trust & Health -->
      <t-card class="run-card" data-testid="trust-summary" :bordered="true" :title="$t('evaluationRun.detail.sections.trust')">
        <div class="kv-grid">
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.trust.runMeasurementStatus') }}</span><span class="kv-value">{{ report.supporting_observation.run_measurement_status }}</span></div>
        </div>
        <h4 class="subtitle">{{ $t('evaluationRun.detail.trust.tenantWindowHealth') }}</h4>
        <div v-if="report.supporting_observation.tenant_window_health" class="kv-grid">
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.trust.healthFrom') }}</span><span class="kv-value">{{ report.supporting_observation.tenant_window_health.from }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.trust.healthTo') }}</span><span class="kv-value">{{ report.supporting_observation.tenant_window_health.to }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.trust.healthStatus') }}</span><span class="kv-value">{{ report.supporting_observation.tenant_window_health.status }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.trust.meteringAttempted') }}</span><span class="kv-value">{{ report.supporting_observation.tenant_window_health.metering_attempted_count }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.trust.meteringPersisted') }}</span><span class="kv-value">{{ report.supporting_observation.tenant_window_health.metering_persisted_count }}</span></div>
          <div class="kv"><span class="kv-label">{{ $t('evaluationRun.detail.trust.meteringFailed') }}</span><span class="kv-value">{{ report.supporting_observation.tenant_window_health.metering_failed_count }}</span></div>
        </div>
        <p v-else class="empty-line">{{ $t('evaluationRun.detail.notAvailable') }}</p>
        <t-alert v-if="report.supporting_observation.tenant_window_health_is_not_run_completeness" theme="info" :message="$t('evaluationRun.detail.trust.healthScopeNote')" class="scope-note" />
      </t-card>

      <!-- Warnings / reason codes -->
      <t-card class="run-card" data-testid="report-warnings" :bordered="true" :title="$t('evaluationRun.detail.warnings')">
        <ul v-if="reportWarnings.length" class="warning-list" role="alert" aria-live="polite">
          <li v-for="w in reportWarnings" :key="`${w.section}:${w.reason_code}`" class="warning-item">
            <code>{{ w.reason_code }}</code>
            <span class="warning-section">{{ $t('evaluationRun.detail.warningSection', { section: w.section }) }}</span>
            <span>{{ w.message }}</span>
          </li>
        </ul>
        <p v-else class="empty-line">{{ $t('evaluationRun.detail.noWarnings') }}</p>
      </t-card>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, shallowRef, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { RefreshIcon } from 'tdesign-icons-vue-next'
import { fetchEvaluationRunReport, type EvaluationRunReport } from '@/api/evaluation'
import {
  availabilityLabelKey,
  availabilityTone,
  formatScore,
  formatWallClock,
  isTerminalRunStatus,
  normalizeReportWarnings,
  runStatusLabelKey,
  shortRunId,
  type AvailabilityTone,
} from './evaluationReportState'
import {
  createEvaluationRunRequestController,
} from './evaluationRunRequestController'

const route = useRoute()
const { t } = useI18n()

const POLL_INTERVAL_MS = 2000
const MAX_POLL_ATTEMPTS = 60

const controller = createEvaluationRunRequestController<EvaluationRunReport>(fetchEvaluationRunReport)
const state = controller.state
const pollAttempts = shallowRef(0)
const pollStopped = shallowRef(false)
let pollTimer: ReturnType<typeof setTimeout> | null = null

const fallback = computed(() => t('evaluationRun.detail.notAvailable'))
const yesLabel = computed(() => t('common.yes'))
const noLabel = computed(() => t('common.no'))

const runId = computed(() => String(route.params.runId ?? ''))
const shortId = computed(() => shortRunId(runId.value))
const report = computed(() => state.value.data)
const reportWarnings = computed(() => normalizeReportWarnings(report.value?.warnings))
const isTerminal = computed(() => isTerminalRunStatus(report.value?.run.status))

const statusTone = computed<AvailabilityTone>(() => {
  switch (report.value?.run.status) {
    case 'COMPLETED': return 'success'
    case 'FAILED':
    case 'INTERRUPTED': return 'danger'
    default: return 'default'
  }
})
const runStatusLabel = computed(() => t(runStatusLabelKey(report.value?.run.status)))

const errorTitle = computed(() => {
  const status = state.value.error?.status
  if (status === 404) return t('evaluationRun.detail.notFound')
  if (status === 403 || status === 401) return t('evaluationRun.detail.forbidden')
  return t('evaluationRun.detail.loadFailed')
})

const retrievalMetrics = computed(() => {
  const r = report.value?.quality.retrieval
  const fallbackText = fallback.value
  if (!r) return []
  return [
    { key: 'precision', labelKey: 'evaluationRun.detail.metrics.precision', value: formatScore(r.precision, fallbackText) },
    { key: 'recall', labelKey: 'evaluationRun.detail.metrics.recall', value: formatScore(r.recall, fallbackText) },
    { key: 'ndcg3', labelKey: 'evaluationRun.detail.metrics.ndcg3', value: formatScore(r.ndcg3, fallbackText) },
    { key: 'ndcg10', labelKey: 'evaluationRun.detail.metrics.ndcg10', value: formatScore(r.ndcg10, fallbackText) },
    { key: 'mrr', labelKey: 'evaluationRun.detail.metrics.mrr', value: formatScore(r.mrr, fallbackText) },
    { key: 'map', labelKey: 'evaluationRun.detail.metrics.map', value: formatScore(r.map, fallbackText) },
  ]
})

const answerMetrics = computed(() => {
  const a = report.value?.quality.answer
  const fallbackText = fallback.value
  if (!a) return []
  return [
    { key: 'bleu1', labelKey: 'evaluationRun.detail.metrics.bleu1', value: formatScore(a.bleu1, fallbackText) },
    { key: 'bleu2', labelKey: 'evaluationRun.detail.metrics.bleu2', value: formatScore(a.bleu2, fallbackText) },
    { key: 'bleu4', labelKey: 'evaluationRun.detail.metrics.bleu4', value: formatScore(a.bleu4, fallbackText) },
    { key: 'rouge1', labelKey: 'evaluationRun.detail.metrics.rouge1', value: formatScore(a.rouge1, fallbackText) },
    { key: 'rouge2', labelKey: 'evaluationRun.detail.metrics.rouge2', value: formatScore(a.rouge2, fallbackText) },
    { key: 'rougel', labelKey: 'evaluationRun.detail.metrics.rougel', value: formatScore(a.rougel, fallbackText) },
  ]
})

const costTotalText = computed(() => {
  const c = report.value?.cost
  if (!c || c.known_cost_total == null) return fallback.value
  const amount = c.known_cost_total
  const text = Number.isInteger(amount) ? String(amount) : amount.toFixed(6)
  return c.currency ? `${text} ${c.currency}` : text
})

const wallClockText = computed(() => formatWallClock(report.value?.latency.evaluation_wall_clock_ms) ?? fallback.value)

function availabilityToneOf(a: string | undefined): AvailabilityTone {
  return availabilityTone(a as any)
}
function availabilityLabelOf(a: string | undefined): string {
  return t(availabilityLabelKey(a as any))
}
function tokenText(value: number | null | undefined): string {
  if (value == null) return fallback.value
  return String(value)
}

function clearPollTimer() {
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
}

async function load(poll: boolean) {
  if (!runId.value) return
  if (poll) pollAttempts.value += 1
  else pollAttempts.value = 0
  await controller.request(runId.value)
  scheduleNextPoll()
}

function scheduleNextPoll() {
  clearPollTimer()
  if (state.value.stale || state.value.status !== 'success' || !state.value.data) return
  if (isTerminalRunStatus(state.value.data.run.status)) {
    pollStopped.value = false
    return
  }
  if (pollAttempts.value >= MAX_POLL_ATTEMPTS) {
    pollStopped.value = true
    return
  }
  pollStopped.value = false
  pollTimer = setTimeout(() => load(true), POLL_INTERVAL_MS)
}

function refresh() {
  pollStopped.value = false
  load(false)
}

watch(runId, () => {
  clearPollTimer()
  pollStopped.value = false
  pollAttempts.value = 0
  load(false)
}, { immediate: true })

onBeforeUnmount(() => {
  clearPollTimer()
  controller.invalidate()
})
</script>

<style scoped>
.evaluation-run-detail {
  padding: 24px;
  max-width: 1080px;
  margin: 0 auto;
}

.section-header__top {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}

.section-description {
  color: var(--td-text-color-secondary, #666);
  margin-top: 4px;
}

.state-block {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 24px 0;
}

.error-detail {
  color: var(--td-text-color-secondary, #666);
  font-size: 13px;
}

.report-body {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.run-banner {
  margin-bottom: 4px;
}

.card-title-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.kv-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 12px 24px;
}

.kv {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.kv-label {
  font-size: 12px;
  color: var(--td-text-color-secondary, #666);
}

.kv-value {
  font-size: 14px;
  word-break: break-all;
}

.kv-value.mono {
  font-family: var(--td-font-family-mono, monospace);
  font-size: 13px;
}

.metric-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
  gap: 12px 24px;
}

.metric {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.metric-label {
  font-size: 12px;
  color: var(--td-text-color-secondary, #666);
}

.metric-value {
  font-size: 18px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.reason-line {
  font-size: 12px;
  color: var(--td-text-color-secondary, #666);
  margin-bottom: 12px;
}

.subtitle {
  margin: 16px 0 8px;
  font-size: 14px;
}

.scope-note {
  margin-top: 12px;
}

.empty-line {
  color: var(--td-text-color-secondary, #666);
  font-size: 13px;
}

.warning-list {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.warning-item {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  font-size: 13px;
}

.warning-item code {
  background: var(--td-bg-color-secondarycontainer, #f3f3f3);
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 12px;
}

.warning-section {
  color: var(--td-text-color-secondary, #666);
}

@media (max-width: 640px) {
  .evaluation-run-detail {
    padding: 16px;
  }
  .kv-grid,
  .metric-grid {
    grid-template-columns: 1fr;
  }
}
</style>
