// Pure, framework-free formatting/derivation for the Evaluation Run Detail
// page (Task010 Step 4).
//
// The backend read model is the single authority for composition: it already
// produces availability + reason codes and never converts UNKNOWN to 0. This
// module therefore only does display concerns — stable i18n keys, TDesign
// badge tones, human duration formatting, and run-id shortening. It never
// recomputes a cost total, a cache denominator, or a quality score.

import type { ReportAvailability } from '@/api/evaluation'

// Terminal run states (mirrors internal/types/evaluation_run.go). A run in
// these states will not change again, so the page stops polling.
export const TERMINAL_RUN_STATUSES = ['COMPLETED', 'FAILED', 'INTERRUPTED'] as const

export function isTerminalRunStatus(status: string | undefined): boolean {
  return !!status && (TERMINAL_RUN_STATUSES as readonly string[]).includes(status)
}

// Short, stable run id for the page header (first 8 chars of the UUID).
export function shortRunId(runId: string): string {
  return runId.length <= 8 ? runId : runId.slice(0, 8)
}

export type AvailabilityTone =
  | 'success'
  | 'warning'
  | 'default'
  | 'danger'

// TDesign tag/badge tone per availability. AVAILABLE/PARTIAL are usable facts;
// NOT_FINAL is an in-flight state; UNSUPPORTED/DISABLED are deliberate
// absence; UNKNOWN is an honest gap (never rendered as 0).
export function availabilityTone(availability: ReportAvailability | undefined): AvailabilityTone {
  switch (availability) {
    case 'AVAILABLE':
      return 'success'
    case 'PARTIAL':
      return 'warning'
    case 'NOT_FINAL':
      return 'default'
    case 'UNSUPPORTED':
    case 'DISABLED':
      return 'default'
    case 'UNKNOWN':
    default:
      return 'danger'
  }
}

// Stable i18n key for an availability value.
export function availabilityLabelKey(availability: ReportAvailability | undefined): string {
  switch (availability) {
    case 'AVAILABLE':
      return 'evaluationRun.availability.AVAILABLE'
    case 'PARTIAL':
      return 'evaluationRun.availability.PARTIAL'
    case 'UNKNOWN':
      return 'evaluationRun.availability.UNKNOWN'
    case 'NOT_FINAL':
      return 'evaluationRun.availability.NOT_FINAL'
    case 'UNSUPPORTED':
      return 'evaluationRun.availability.UNSUPPORTED'
    case 'DISABLED':
      return 'evaluationRun.availability.DISABLED'
    default:
      return 'evaluationRun.availability.UNKNOWN'
  }
}

// Stable i18n key for a run lifecycle status.
export function runStatusLabelKey(status: string | undefined): string {
  switch (status) {
    case 'PENDING':
      return 'evaluationRun.status.PENDING'
    case 'RUNNING':
      return 'evaluationRun.status.RUNNING'
    case 'COMPLETED':
      return 'evaluationRun.status.COMPLETED'
    case 'FAILED':
      return 'evaluationRun.status.FAILED'
    case 'INTERRUPTED':
      return 'evaluationRun.status.INTERRUPTED'
    default:
      return 'evaluationRun.status.UNKNOWN'
  }
}

// Human-readable wall-clock duration from milliseconds. Returns null for a
// non-positive duration so callers render "unknown" rather than "0 ms".
export function formatWallClock(ms: number | null | undefined): string | null {
  if (ms == null || ms < 0) return null
  if (ms < 1000) return `${ms} ms`
  const seconds = ms / 1000
  if (seconds < 60) return `${seconds.toFixed(1)} s`
  const minutes = seconds / 60
  if (minutes < 60) return `${Math.floor(minutes)}m ${Math.round(seconds % 60)}s`
  const hours = minutes / 60
  return `${hours.toFixed(2)} h`
}

// Format a possibly-absent number. A missing/unknown value is rendered as the
// caller-provided fallback, never as 0.
export function formatNumber(value: number | null | undefined, fallback: string): string {
  if (value == null || Number.isNaN(value)) return fallback
  return String(value)
}

// Format a metric score to a fixed, bounded precision. Scores are real floats;
// truncate to 4 decimals for readability but never fabricate a value.
export function formatScore(value: number | null | undefined, fallback: string): string {
  if (value == null || Number.isNaN(value)) return fallback
  return value.toFixed(4)
}

// Older report producers and partially rolled-out deployments may encode an
// empty Go slice as JSON null. Keep the component boundary total and render an
// empty warning list rather than throwing on `.length`.
export function normalizeReportWarnings<T>(warnings: T[] | null | undefined): T[] {
  return Array.isArray(warnings) ? warnings : []
}
