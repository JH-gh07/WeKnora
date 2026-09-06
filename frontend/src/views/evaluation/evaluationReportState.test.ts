import assert from 'node:assert/strict'
import test from 'node:test'

import {
  availabilityLabelKey,
  availabilityTone,
  formatNumber,
  formatScore,
  formatWallClock,
  isTerminalRunStatus,
  normalizeReportWarnings,
  runStatusLabelKey,
  shortRunId,
} from './evaluationReportState.ts'

test('shortRunId truncates to 8 chars and preserves short ids', () => {
  assert.equal(shortRunId('12345678-1234-1234-1234-123456789abc'), '12345678')
  assert.equal(shortRunId('abc'), 'abc')
})

test('warning normalization accepts legacy null without fabricating entries', () => {
  assert.deepEqual(normalizeReportWarnings(null), [])
  assert.deepEqual(normalizeReportWarnings(undefined), [])
  const warnings = [{ reason_code: 'X' }]
  assert.equal(normalizeReportWarnings(warnings), warnings)
})

test('isTerminalRunStatus matches frozen terminal states', () => {
  for (const s of ['COMPLETED', 'FAILED', 'INTERRUPTED']) {
    assert.equal(isTerminalRunStatus(s), true, s)
  }
  for (const s of ['PENDING', 'RUNNING', undefined, '']) {
    assert.equal(isTerminalRunStatus(s), false, String(s))
  }
})

test('runStatusLabelKey maps every status and defaults to UNKNOWN', () => {
  assert.equal(runStatusLabelKey('PENDING'), 'evaluationRun.status.PENDING')
  assert.equal(runStatusLabelKey('RUNNING'), 'evaluationRun.status.RUNNING')
  assert.equal(runStatusLabelKey('COMPLETED'), 'evaluationRun.status.COMPLETED')
  assert.equal(runStatusLabelKey('FAILED'), 'evaluationRun.status.FAILED')
  assert.equal(runStatusLabelKey('INTERRUPTED'), 'evaluationRun.status.INTERRUPTED')
  assert.equal(runStatusLabelKey('BOGUS'), 'evaluationRun.status.UNKNOWN')
  assert.equal(runStatusLabelKey(undefined), 'evaluationRun.status.UNKNOWN')
})

test('availabilityLabelKey and tone cover the frozen vocabulary', () => {
  assert.equal(availabilityLabelKey('AVAILABLE'), 'evaluationRun.availability.AVAILABLE')
  assert.equal(availabilityTone('AVAILABLE'), 'success')
  assert.equal(availabilityTone('PARTIAL'), 'warning')
  assert.equal(availabilityTone('NOT_FINAL'), 'default')
  assert.equal(availabilityTone('UNSUPPORTED'), 'default')
  assert.equal(availabilityTone('DISABLED'), 'default')
  assert.equal(availabilityTone('UNKNOWN'), 'danger')
  assert.equal(availabilityTone(undefined), 'danger')
})

test('formatWallClock renders human durations and never fabricates zero', () => {
  assert.equal(formatWallClock(null), null)
  assert.equal(formatWallClock(undefined), null)
  assert.equal(formatWallClock(-1), null)
  assert.equal(formatWallClock(0), '0 ms')
  assert.equal(formatWallClock(500), '500 ms')
  assert.equal(formatWallClock(1500), '1.5 s')
  assert.match(formatWallClock(90_000)!, /^1m 30s$/)
  assert.match(formatWallClock(3_600_000)!, /^1\.00 h$/)
})

test('formatNumber and formatScore never coerce a missing value to 0', () => {
  assert.equal(formatNumber(0, '-'), '0')
  assert.equal(formatNumber(null, '-'), '-')
  assert.equal(formatNumber(undefined, '-'), '-')
  assert.equal(formatScore(0.123456, '-'), '0.1235')
  assert.equal(formatScore(null, '-'), '-')
  assert.equal(formatScore(undefined, '-'), '-')
})
