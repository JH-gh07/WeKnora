<template>
  <section class="model-usage" aria-labelledby="model-usage-title">
    <header class="model-usage__header">
      <div>
        <h3 id="model-usage-title" class="model-usage__title">{{ $t('modelUsage.title') }}</h3>
        <p class="model-usage__desc">{{ $t('modelUsage.description') }}</p>
      </div>
    </header>

    <div class="model-usage__controls">
      <div class="control">
        <label class="control__label" for="model-usage-model">{{ $t('modelUsage.modelLabel') }}</label>
        <t-select
          id="model-usage-model"
          v-model="selectedModelId"
          :options="modelOptions"
          :style="{ width: '220px' }"
          aria-label="model-usage-model"
        />
      </div>
      <div class="control">
        <span class="control__label" id="model-usage-preset-label">{{ $t('modelUsage.timePreset') }}</span>
        <t-radio-group v-model="preset" variant="default-filled" aria-labelledby="model-usage-preset-label">
          <t-radio-button v-for="opt in presetOptions" :key="opt.value" :value="opt.value">
            {{ opt.label }}
          </t-radio-button>
        </t-radio-group>
      </div>
    </div>

    <div class="model-usage__window" role="note">
      <span class="window__label">{{ $t('modelUsage.windowLabel') }}</span>
      <code class="window__range">{{ currentWindow.from }} → {{ currentWindow.to }}</code>
    </div>

    <p class="model-usage__boundary" role="note">{{ $t('modelUsage.measurementBoundary') }}</p>

    <t-loading :loading="uiState.status === 'loading'" size="small" show-overlay>
      <div v-if="uiState.status === 'error'" class="model-usage__error" role="alert">
        <p>{{ $t('modelUsage.error') }}</p>
        <t-button size="small" theme="primary" variant="outline" @click="retry">
          {{ $t('modelUsage.retry') }}
        </t-button>
      </div>

      <template v-else-if="viewModel">
        <div v-if="viewModel.anomalies.length > 0" class="model-usage__anomalies" role="alert">
          <t-icon name="error-circle" size="16px" />
          <span>{{ $t('modelUsage.contractAnomaly') }}</span>
        </div>

        <!-- Calls area: Empty only replaces the call counts; Availability metrics
             and Measurement Health always render independently (B13/B15). -->
        <div v-if="viewModel.logicalCalls.value === 0" class="model-usage__empty">
          <t-icon name="chart-bar" size="28px" />
          <p>{{ $t('modelUsage.empty') }}</p>
        </div>
        <div v-else class="model-usage__grid">
          <div class="fact">
            <span class="fact__label">{{ $t('modelUsage.logicalCalls') }}</span>
            <span class="fact__value">{{ factValue(viewModel.logicalCalls) }}</span>
          </div>
          <div class="fact">
            <span class="fact__label">{{ $t('modelUsage.success') }}</span>
            <span class="fact__value fact__value--success">{{ factValue(viewModel.success) }}</span>
          </div>
          <div class="fact">
            <span class="fact__label">{{ $t('modelUsage.failure') }}</span>
            <span class="fact__value fact__value--failure">{{ factValue(viewModel.failure) }}</span>
          </div>
        </div>

        <dl class="model-usage__metrics">
          <div class="metric">
            <dt class="metric__label">
              {{ $t('modelUsage.promptCacheCoverage') }}
              <t-tag :theme="availabilityTheme(viewModel.promptCacheCoverage.availability)" variant="light" size="small">
                {{ availabilityLabel(viewModel.promptCacheCoverage.availability) }}
              </t-tag>
            </dt>
            <dd class="metric__value">
              <span v-if="viewModel.promptCacheCoverage.availability === 'AVAILABLE'">
                {{ fmtRatio(viewModel.promptCacheCoverage.ratio ?? 0) }}
                <span class="metric__detail">({{ viewModel.promptCacheCoverage.numerator }}/{{ viewModel.promptCacheCoverage.denominator }})</span>
              </span>
              <span v-else class="metric__detail">{{ availabilityLabel(viewModel.promptCacheCoverage.availability) }}</span>
            </dd>
          </div>

          <div class="metric">
            <dt class="metric__label">
              {{ $t('modelUsage.cachedTokenRatio') }}
              <t-tag :theme="availabilityTheme(viewModel.cachedTokenRatio.availability)" variant="light" size="small">
                {{ availabilityLabel(viewModel.cachedTokenRatio.availability) }}
              </t-tag>
            </dt>
            <dd class="metric__value">
              <span v-if="viewModel.cachedTokenRatio.availability === 'AVAILABLE'">
                {{ fmtRatio(viewModel.cachedTokenRatio.value ?? 0) }}
              </span>
              <span v-else class="metric__detail">{{ availabilityLabel(viewModel.cachedTokenRatio.availability) }}</span>
            </dd>
          </div>

          <div class="metric">
            <dt class="metric__label">
              {{ $t('modelUsage.localEmbeddingCache') }}
              <t-tag :theme="availabilityTheme(viewModel.localEmbeddingCache.availability)" variant="light" size="small">
                {{ availabilityLabel(viewModel.localEmbeddingCache.availability) }}
              </t-tag>
            </dt>
            <dd class="metric__value">
              <span v-if="viewModel.localEmbeddingCache.availability === 'AVAILABLE'">
                {{ fmtRatio(viewModel.localEmbeddingCache.hitRate ?? 0) }}
                <span class="metric__detail">({{ viewModel.localEmbeddingCache.hitCount }}/{{ viewModel.localEmbeddingCache.logicalItemCount }})</span>
              </span>
              <span v-else class="metric__detail">{{ availabilityLabel(viewModel.localEmbeddingCache.availability) }}</span>
            </dd>
            <dd v-if="viewModel.localEmbeddingCache.availability !== 'DISABLED'" class="metric__detail metric__warnings">
              <span v-if="viewModel.localEmbeddingCache.lookupFailedCount > 0">
                {{ $t('modelUsage.localLookupFailed', { count: viewModel.localEmbeddingCache.lookupFailedCount }) }}
              </span>
              <span v-if="viewModel.localEmbeddingCache.corruptionCount > 0">
                {{ $t('modelUsage.localCorruption', { count: viewModel.localEmbeddingCache.corruptionCount }) }}
              </span>
              <span v-if="viewModel.localEmbeddingCache.writeFailedCount > 0">
                {{ $t('modelUsage.localWriteFailed', { count: viewModel.localEmbeddingCache.writeFailedCount }) }}
              </span>
            </dd>
          </div>

          <div class="metric">
            <dt class="metric__label">
              {{ $t('modelUsage.knownCost') }}
              <t-tag :theme="availabilityTheme(viewModel.cost.availability)" variant="light" size="small">
                {{ availabilityLabel(viewModel.cost.availability) }}
              </t-tag>
            </dt>
            <dd class="metric__value">
              <span v-if="viewModel.cost.windowEmpty" class="metric__detail">{{ $t('modelUsage.empty') }}</span>
              <span v-else-if="viewModel.cost.availability === 'AVAILABLE' && viewModel.cost.knownCostTotal != null && viewModel.cost.currency" class="metric__value">
                {{ fmtCost(viewModel.cost.knownCostTotal, viewModel.cost.currency) }}
              </span>
              <span v-else class="metric__detail">{{ availabilityLabel(viewModel.cost.availability) }}</span>
              <span
                v-if="viewModel.cost.unknownCostCallCount > 0"
                class="metric__sub"
              >{{ $t('modelUsage.unknownPriceCalls', { count: viewModel.cost.unknownCostCallCount }) }}</span>
            </dd>
          </div>
        </dl>

        <p class="model-usage__disclaimer" role="note">{{ $t('modelUsage.costDisclaimer') }}</p>

        <div class="model-usage__health">
          <span class="health__label">{{ $t('modelUsage.measurementHealth') }}</span>
          <t-tag :theme="healthTheme(viewModel.health.status)" variant="light" size="small">
            {{ healthLabel(viewModel.health.status) }}
          </t-tag>
          <span class="health__counts">
            {{ $t('modelUsage.attempted') }} {{ fmtCount(viewModel.health.attempted) }} ·
            {{ $t('modelUsage.persisted') }} {{ fmtCount(viewModel.health.persisted) }} ·
            {{ $t('modelUsage.failed') }} {{ fmtCount(viewModel.health.failed) }}
          </span>
        </div>
      </template>
    </t-loading>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { fetchModelUsage } from '@/api/model-usage'
import type { ModelUsageAggregate } from '@/api/model-usage'
import {
  buildUtcWindow,
  deriveModelUsageViewModel,
  type AvailabilityState,
  type TimePreset,
  type UtcWindow,
} from '../modelUsageState'
import {
  createModelUsageRequestController,
  type ModelUsageRequestSnapshot,
  type ModelUsageRequestState,
} from '../modelUsageRequestController'

interface MeteredModel {
  id: string
  name: string
  displayName: string
  type: 'chat' | 'embedding' | 'rerank'
}

const props = defineProps<{ models: MeteredModel[] }>()

const { t } = useI18n()
const authStore = useAuthStore()

const selectedModelId = ref<string>('')
const preset = ref<TimePreset>('24h')
const currentWindow = ref<UtcWindow>(buildUtcWindow('24h'))

const controller = createModelUsageRequestController<ModelUsageAggregate>((snapshot) =>
  fetchModelUsage({
    model_id: snapshot.modelId || undefined,
    from: snapshot.from,
    to: snapshot.to,
  }),
)

const uiState = ref<ModelUsageRequestState<ModelUsageAggregate>>(controller.getState())
const sync = () => {
  uiState.value = controller.getState()
}

const viewModel = computed(() => {
  const data = uiState.value.data
  return data ? deriveModelUsageViewModel(data) : null
})

const modelOptions = computed(() => [
  { label: t('modelUsage.allMetered'), value: '' },
  ...props.models.map((m) => ({ label: m.displayName || m.name, value: m.id })),
])

const presetOptions: Array<{ label: string; value: TimePreset }> = [
  { label: '24h', value: '24h' },
  { label: '7d', value: '7d' },
  { label: '30d', value: '30d' },
]

async function load() {
  const win = buildUtcWindow(preset.value)
  currentWindow.value = win
  const snapshot: ModelUsageRequestSnapshot = {
    tenantId: authStore.effectiveTenantId ?? null,
    modelId: selectedModelId.value || null,
    preset: preset.value,
    from: win.from,
    to: win.to,
  }
  const p = controller.request(snapshot)
  sync()
  await p
  sync()
}

function retry() {
  void load()
}

watch(
  [() => authStore.effectiveTenantId, selectedModelId, preset],
  () => {
    void load()
  },
  { immediate: true },
)

function fmtCount(n: number): string {
  return n.toLocaleString()
}

function fmtRatio(r: number): string {
  return `${(r * 100).toFixed(1)}%`
}

function fmtCost(value: number, currency: string): string {
  return `${currency} ${value.toFixed(6)}`
}

// A count fact is only rendered as a number when AVAILABLE; any non-AVAILABLE
// state (e.g. UNKNOWN on a contract anomaly) renders its label instead of 0.
function factValue(fact: { availability: AvailabilityState; value: number | null }): string {
  if (fact.availability === 'AVAILABLE' && fact.value != null) return fmtCount(fact.value)
  return availabilityLabel(fact.availability)
}

const AVAILABILITY_LABEL_KEYS: Record<AvailabilityState, string> = {
  AVAILABLE: 'modelUsage.availability.available',
  NOT_IMPLEMENTED: 'modelUsage.availability.notImplemented',
  UNSUPPORTED: 'modelUsage.availability.unsupported',
  UNREPORTED: 'modelUsage.availability.unreported',
  UNKNOWN: 'modelUsage.availability.unknown',
  DISABLED: 'modelUsage.availability.disabled',
}

function availabilityLabel(state: AvailabilityState): string {
  return t(AVAILABILITY_LABEL_KEYS[state])
}

function availabilityTheme(state: AvailabilityState): string {
  switch (state) {
    case 'AVAILABLE':
      return 'success'
    case 'UNREPORTED':
      return 'warning'
    case 'DISABLED':
      return 'default'
    case 'UNSUPPORTED':
    case 'NOT_IMPLEMENTED':
    case 'UNKNOWN':
    default:
      return 'default'
  }
}

const HEALTH_LABEL_KEYS: Record<string, string> = {
  COMPLETE: 'modelUsage.health.complete',
  PARTIAL: 'modelUsage.health.partial',
  UNKNOWN: 'modelUsage.health.unknown',
}

function healthLabel(status: string): string {
  return t(HEALTH_LABEL_KEYS[status] ?? HEALTH_LABEL_KEYS.UNKNOWN)
}

function healthTheme(status: string): string {
  if (status === 'COMPLETE') return 'success'
  if (status === 'PARTIAL') return 'warning'
  return 'default'
}
</script>

<style lang="less" scoped>
.model-usage {
  border: 1px solid var(--td-component-stroke);
  border-radius: 10px;
  padding: 16px 18px;
  margin-bottom: 18px;
  background: var(--td-bg-color-container);

  &__header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
  }

  &__title {
    font-size: 16px;
    font-weight: 600;
    margin: 0 0 4px;
  }

  &__desc {
    color: var(--td-text-color-secondary);
    font-size: 13px;
    margin: 0;
  }

  &__controls {
    display: flex;
    flex-wrap: wrap;
    gap: 18px;
    margin-top: 14px;
  }

  &__window {
    margin-top: 12px;
    font-size: 12px;
    color: var(--td-text-color-secondary);
  }

  &__boundary {
    margin: 6px 0 0;
    font-size: 12px;
    color: var(--td-text-color-placeholder);
  }

  &__error {
    padding: 16px 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
    align-items: flex-start;
  }

  &__anomalies {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 14px;
    padding: 8px 12px;
    border: 1px solid var(--td-warning-color);
    border-radius: 6px;
    background: color-mix(in srgb, var(--td-warning-color) 10%, transparent);
    color: var(--td-warning-color);
    font-size: 13px;
  }

  &__empty {
    padding: 20px 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
    align-items: center;
    color: var(--td-text-color-placeholder);
  }

  &__grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 12px;
    margin-top: 16px;
  }

  &__metrics {
    margin: 14px 0 0;
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 12px;
  }

  &__disclaimer {
    margin: 10px 0 0;
    font-size: 12px;
    color: var(--td-text-color-placeholder);
  }

  &__health {
    margin-top: 14px;
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
    align-items: center;
    font-size: 13px;
  }
}

.control {
  display: flex;
  flex-direction: column;
  gap: 6px;

  &__label {
    font-size: 12px;
    color: var(--td-text-color-secondary);
  }
}

.window__label {
  margin-right: 6px;
  font-weight: 500;
}

.window__range {
  font-family: monospace;
  background: var(--td-bg-color-secondarycontainer);
  padding: 2px 6px;
  border-radius: 4px;
}

.fact {
  border: 1px solid var(--td-component-stroke);
  border-radius: 8px;
  padding: 10px 12px;

  &__label {
    display: block;
    font-size: 12px;
    color: var(--td-text-color-secondary);
  }

  &__value {
    display: block;
    font-size: 20px;
    font-weight: 600;
    margin-top: 4px;

    &--success {
      color: var(--td-success-color);
    }

    &--failure {
      color: var(--td-error-color);
    }
  }
}

.metric {
  border: 1px solid var(--td-component-stroke);
  border-radius: 8px;
  padding: 10px 12px;

  &__label {
    display: flex;
    gap: 6px;
    align-items: center;
    font-size: 12px;
    color: var(--td-text-color-secondary);
  }

  &__value {
    margin: 6px 0 0;
    font-size: 15px;
    font-weight: 500;
  }

  &__detail {
    color: var(--td-text-color-secondary);
    font-size: 13px;
  }

  &__sub {
    display: block;
    margin-top: 4px;
    font-size: 12px;
    color: var(--td-text-color-placeholder);
  }
}

.health__label {
  font-weight: 500;
}

.health__counts {
  color: var(--td-text-color-secondary);
}
</style>
