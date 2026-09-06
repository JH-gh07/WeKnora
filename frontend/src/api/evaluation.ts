// Evaluation API consumer — Task010 (Gate AC-R3_RUN_REPORT).
//
// Read-only consumer of the run-level unified report endpoint. It MUST NOT
// recompute quality, cost or cache denominators; it only carries the backend's
// typed JSON contract into the frontend and leaves the number semantics to
// `views/evaluation/evaluationReportState.ts`. The backend read model is the
// single authority for composition (Decision 010-3).

import { get } from '@/utils/request'

// Mirrors internal/types/evaluation_report.go ReportAvailability (§4.3).
export type ReportAvailability =
  | 'AVAILABLE'
  | 'PARTIAL'
  | 'UNKNOWN'
  | 'NOT_FINAL'
  | 'UNSUPPORTED'
  | 'DISABLED'

export interface ReportWarning {
  reason_code: string
  section: string
  message: string
}

export interface ReportRunSection {
  run_id: string
  legacy_task_id: string
  status: string
  started_at?: string
  ended_at?: string
  protocol_hash?: string
  git_commit?: string
  app_version?: string
  persistence_status?: string
  cleanup_status?: string
  interruption_reason?: string
}

export interface RetrievalMetrics {
  precision: number
  recall: number
  ndcg3: number
  ndcg10: number
  mrr: number
  map: number
}

export interface GenerationMetrics {
  bleu1: number
  bleu2: number
  bleu4: number
  rouge1: number
  rouge2: number
  rougel: number
}

export interface ReportQualitySection {
  availability: ReportAvailability
  reason_code?: string
  metrics_valid: boolean
  retrieval?: RetrievalMetrics
  answer?: GenerationMetrics
}

export interface ReportUsageSection {
  availability: ReportAvailability
  reason_code?: string
  logical_call_count: number
  success_count: number
  failure_count: number
  input_tokens?: number | null
  output_tokens?: number | null
}

export interface ReportCostSection {
  availability: ReportAvailability
  reason_code?: string
  known_cost_total?: number | null
  currency?: string | null
  unknown_cost_call_count: number
  mixed_currency: boolean
  is_estimate: boolean
}

export interface ReportLatencySection {
  availability: ReportAvailability
  reason_code?: string
  evaluation_wall_clock_ms?: number | null
  definition: string
}

export interface ReportPromptCacheSupport {
  eligible_count: number
  reported_count: number
  unsupported_count: number
  cache_read_tokens?: number | null
  cache_write_tokens?: number | null
  cache_miss_tokens?: number | null
  cache_reported_input_tokens?: number | null
}

export interface ReportLocalCacheSupport {
  implementation_status: string
  batch_invocation_count: number
  logical_item_count: number
  hit_count: number
  miss_count: number
  measurement_status: string
}

export interface ReportMeasurementHealth {
  from: string
  to: string
  status: string
  metering_attempted_count: number
  metering_persisted_count: number
  metering_failed_count: number
}

export interface ReportSupportingSection {
  prompt_cache?: ReportPromptCacheSupport | null
  local_embedding_cache?: ReportLocalCacheSupport | null
  run_measurement_status: string
  tenant_window_health?: ReportMeasurementHealth | null
  tenant_window_health_is_not_run_completeness: boolean
}

export interface EvaluationRunReport {
  schema_version: string
  run: ReportRunSection
  quality: ReportQualitySection
  usage: ReportUsageSection
  cost: ReportCostSection
  latency: ReportLatencySection
  supporting_observation: ReportSupportingSection
  warnings: ReportWarning[]
}

interface EvaluationRunReportEnvelope {
  success: boolean
  data: EvaluationRunReport
  message?: string
}

// Transport errors from the axios interceptor arrive as `{ status, message }`;
// normalize everything into that shape so the page can distinguish 404 (not
// found) / 403 (forbidden) from a generic server error.
export interface EvaluationReportError {
  status?: number
  message: string
}

export function fetchEvaluationRunReport(runId: string): Promise<EvaluationRunReport> {
  const url = `/api/v1/evaluation/runs/${encodeURIComponent(runId)}/report`
  return get<EvaluationRunReportEnvelope>(url)
    .then((env) => {
      if (env && env.success && env.data) return env.data
      throw { status: undefined, message: env?.message || 'evaluation run report request failed' }
    })
    .catch((err: unknown) => {
      if (typeof err === 'object' && err !== null) {
        const anyErr = err as { status?: unknown; message?: unknown }
        const status = typeof anyErr.status === 'number' ? anyErr.status : undefined
        const message = typeof anyErr.message === 'string' ? anyErr.message : String(err)
        throw { status, message } as EvaluationReportError
      }
      throw { status: undefined, message: String(err) } as EvaluationReportError
    })
}
