<template>
  <div class="evaluation-run-lookup">
    <div class="section-header">
      <h2>{{ $t('evaluationRun.lookup.title') }}</h2>
      <p class="section-description">{{ $t('evaluationRun.lookup.description') }}</p>
    </div>

    <t-card class="lookup-card" :bordered="true">
      <div class="lookup-form">
        <t-input
          v-model="runId"
          :placeholder="$t('evaluationRun.lookup.placeholder')"
          clearable
          class="lookup-input"
          @enter="submit"
        />
        <t-button theme="primary" :disabled="!canSubmit" @click="submit">
          {{ $t('evaluationRun.lookup.submit') }}
        </t-button>
      </div>
      <p v-if="error" class="lookup-error">{{ error }}</p>
    </t-card>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

const router = useRouter()
const { t } = useI18n()

const runId = ref('')
const error = ref('')

// run_id is always a UUID (allocated in EvaluationRun.BeforeCreate). A
// malformed value is rejected before navigation; the backend also returns 400.
const UUID_RE = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/

const trimmed = computed(() => runId.value.trim())
const canSubmit = computed(() => trimmed.value.length > 0)

function submit() {
  const value = trimmed.value
  if (!value) {
    error.value = t('evaluationRun.lookup.empty')
    return
  }
  if (!UUID_RE.test(value)) {
    error.value = t('evaluationRun.lookup.invalid')
    return
  }
  error.value = ''
  router.push({ name: 'evaluationRunDetail', params: { runId: value } })
}
</script>

<style scoped>
.evaluation-run-lookup {
  padding: 24px;
  max-width: 720px;
  margin: 0 auto;
}

.section-description {
  color: var(--td-text-color-secondary, #666);
  margin-top: 4px;
}

.lookup-card {
  margin-top: 16px;
}

.lookup-form {
  display: flex;
  gap: 12px;
  align-items: center;
}

.lookup-input {
  flex: 1;
}

.lookup-error {
  color: var(--td-error-color, #d54941);
  font-size: 13px;
  margin-top: 8px;
}

@media (max-width: 640px) {
  .lookup-form {
    flex-direction: column;
    align-items: stretch;
  }
}
</style>
