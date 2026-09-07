package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ---- fakes -------------------------------------------------------------

type reportFakeRunRepository struct {
	interfaces.EvaluationRunRepository
	run *types.EvaluationRun
	err error
}

func (f *reportFakeRunRepository) GetByRunID(_ context.Context, _ uint64, _ string) (*types.EvaluationRun, error) {
	return f.run, f.err
}

type reportFakeUsageService struct {
	interfaces.ModelUsageService
	agg    *types.ModelUsageAggregate
	aggErr error
	health *types.MeasurementHealth
	hlErr  error
}

func (f *reportFakeUsageService) Aggregate(_ context.Context, _ types.ModelCallFilter) (*types.ModelUsageAggregate, error) {
	return f.agg, f.aggErr
}

func (f *reportFakeUsageService) Health(_ context.Context, _ uint64, _, _ time.Time) (*types.MeasurementHealth, error) {
	return f.health, f.hlErr
}

func newReportService(run *types.EvaluationRun, agg *types.ModelUsageAggregate, health *types.MeasurementHealth) *evaluationReportService {
	return &evaluationReportService{
		runRepository: &reportFakeRunRepository{run: run},
		usageService:  &reportFakeUsageService{agg: agg, health: health},
	}
}

// ---- helpers -----------------------------------------------------------

func strPtr(s string) *string     { return &s }
func int64Ptr(i int64) *int64     { return &i }
func floatPtr(f float64) *float64 { return &f }

func completedRun() *types.EvaluationRun {
	start := time.Now().Add(-10 * time.Second)
	end := time.Now()
	return &types.EvaluationRun{
		RunID:             "00000000-0000-0000-0000-000000000001",
		TaskID:            "evaluation-1-default",
		TenantID:          1,
		Status:            types.EvaluationRunStatusCompleted,
		StartedAt:         &start,
		EndedAt:           &end,
		MetricsValid:      true,
		MetricsJSON:       types.JSON(`{"retrieval_metrics":{"precision":0.5,"recall":0.6,"ndcg3":0.4,"ndcg10":0.5,"mrr":0.5,"map":0.5},"generation_metrics":{"bleu1":0.1,"bleu2":0.2,"bleu4":0.3,"rouge1":0.4,"rouge2":0.5,"rougel":0.6}}`),
		MeasurementStatus: types.MeasurementStatusUnknown,
		PersistenceStatus: types.PersistenceStatusPersisted,
		CleanupStatus:     types.CleanupStatusDone,
	}
}

// ---- cost truth table (plan §5.1) ---------------------------------------

func TestDeriveCostTruthTable(t *testing.T) {
	cases := []struct {
		name   string
		agg    *types.ModelUsageAggregate
		avail  types.ReportAvailability
		reason string
	}{
		{"no calls", &types.ModelUsageAggregate{LogicalCallCount: 0}, types.AvailabilityUnknown, types.ReasonNoObservedModelCall},
		{"all unknown", &types.ModelUsageAggregate{LogicalCallCount: 5, UnknownCostCallCount: 5}, types.AvailabilityUnknown, types.ReasonAllCostUnknown},
		{"partial known", &types.ModelUsageAggregate{LogicalCallCount: 5, KnownCostTotal: floatPtr(1.25), Currency: strPtr("CNY"), UnknownCostCallCount: 2}, types.AvailabilityPartial, types.ReasonSomeCostUnknown},
		{"all known", &types.ModelUsageAggregate{LogicalCallCount: 5, KnownCostTotal: floatPtr(1.25), Currency: strPtr("CNY")}, types.AvailabilityAvailable, ""},
		{"mixed currency", &types.ModelUsageAggregate{LogicalCallCount: 5, MixedCurrency: true}, types.AvailabilityUnknown, types.ReasonMixedCurrency},
		{"true zero", &types.ModelUsageAggregate{LogicalCallCount: 5, KnownCostTotal: floatPtr(0), Currency: strPtr("CNY")}, types.AvailabilityAvailable, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveCostSection(tc.agg)
			if got.Availability != tc.avail {
				t.Fatalf("availability: got %s want %s", got.Availability, tc.avail)
			}
			if tc.reason != "" && got.ReasonCode != tc.reason {
				t.Fatalf("reason: got %s want %s", got.ReasonCode, tc.reason)
			}
			if !got.IsEstimate {
				t.Fatalf("cost must always be marked estimate")
			}
		})
	}
}

// ---- quality three states ----------------------------------------------

func TestDeriveQualitySection(t *testing.T) {
	t.Run("running is NOT_FINAL", func(t *testing.T) {
		run := completedRun()
		run.Status = types.EvaluationRunStatusRunning
		q := deriveQualitySection(run)
		if q.Availability != types.AvailabilityNotFinal || q.ReasonCode != types.ReasonRunNotTerminal {
			t.Fatalf("unexpected: %+v", q)
		}
	})
	t.Run("invalid metrics is UNKNOWN", func(t *testing.T) {
		run := completedRun()
		run.MetricsValid = false
		q := deriveQualitySection(run)
		if q.Availability != types.AvailabilityUnknown || q.ReasonCode != types.ReasonMetricsInvalid {
			t.Fatalf("unexpected: %+v", q)
		}
		if q.Retrieval != nil {
			t.Fatalf("must not emit retrieval when invalid")
		}
	})
	t.Run("malformed metrics is UNKNOWN no panic", func(t *testing.T) {
		run := completedRun()
		run.MetricsJSON = types.JSON(`not-json`)
		q := deriveQualitySection(run)
		if q.Availability != types.AvailabilityUnknown || q.ReasonCode != types.ReasonMetricsMalformed {
			t.Fatalf("unexpected: %+v", q)
		}
	})
	t.Run("missing metric fields are UNKNOWN rather than fabricated zeroes", func(t *testing.T) {
		run := completedRun()
		run.MetricsJSON = types.JSON(`{}`)
		q := deriveQualitySection(run)
		if q.Availability != types.AvailabilityUnknown || q.ReasonCode != types.ReasonMetricsMalformed {
			t.Fatalf("unexpected: %+v", q)
		}
		if q.Retrieval != nil || q.Answer != nil {
			t.Fatalf("incomplete metrics must not be emitted: %+v", q)
		}
	})
	t.Run("null metric values are UNKNOWN rather than fabricated zeroes", func(t *testing.T) {
		run := completedRun()
		run.MetricsJSON = types.JSON(`{"retrieval_metrics":{"precision":null,"recall":0.2,"ndcg3":0.3,"ndcg10":0.4,"mrr":0.5,"map":0.6},"generation_metrics":{"bleu1":0.1,"bleu2":0.2,"bleu4":0.3,"rouge1":0.4,"rouge2":0.5,"rougel":0.6}}`)
		q := deriveQualitySection(run)
		if q.Availability != types.AvailabilityUnknown || q.ReasonCode != types.ReasonMetricsMalformed {
			t.Fatalf("unexpected: %+v", q)
		}
	})
	t.Run("valid metrics AVAILABLE with real values", func(t *testing.T) {
		q := deriveQualitySection(completedRun())
		if q.Availability != types.AvailabilityAvailable || q.ReasonCode != types.ReasonQualityComplete {
			t.Fatalf("unexpected: %+v", q)
		}
		if q.Retrieval == nil || q.Answer == nil {
			t.Fatalf("expected both metric sets")
		}
		if q.Retrieval.Precision != 0.5 {
			t.Fatalf("expected precision 0.5, got %v", q.Retrieval.Precision)
		}
	})
}

// ---- latency three states ----------------------------------------------

func TestDeriveLatencySection(t *testing.T) {
	t.Run("running NOT_FINAL", func(t *testing.T) {
		run := completedRun()
		run.Status = types.EvaluationRunStatusRunning
		l := deriveLatencySection(run)
		if l.Availability != types.AvailabilityNotFinal {
			t.Fatalf("unexpected: %+v", l)
		}
	})
	t.Run("missing timestamp UNKNOWN", func(t *testing.T) {
		run := completedRun()
		run.EndedAt = nil
		l := deriveLatencySection(run)
		if l.Availability != types.AvailabilityUnknown || l.ReasonCode != types.ReasonTimestampMissing {
			t.Fatalf("unexpected: %+v", l)
		}
	})
	t.Run("available wall clock", func(t *testing.T) {
		l := deriveLatencySection(completedRun())
		if l.Availability != types.AvailabilityAvailable || l.EvaluationWallClockMS == nil {
			t.Fatalf("unexpected: %+v", l)
		}
		if l.Definition != "ended_at - started_at" {
			t.Fatalf("definition must state wall-clock semantics")
		}
	})
}

// ---- usage two states ---------------------------------------------------

func TestDeriveUsageSection(t *testing.T) {
	t.Run("no observed calls UNKNOWN", func(t *testing.T) {
		u := deriveUsageSection(&types.ModelUsageAggregate{LogicalCallCount: 0})
		if u.Availability != types.AvailabilityUnknown || u.ReasonCode != types.ReasonNoObservedModelCall {
			t.Fatalf("unexpected: %+v", u)
		}
	})
	t.Run("observed calls AVAILABLE", func(t *testing.T) {
		u := deriveUsageSection(&types.ModelUsageAggregate{LogicalCallCount: 5, SuccessCount: 4, FailureCount: 1, InputTokens: int64Ptr(100)})
		if u.Availability != types.AvailabilityAvailable {
			t.Fatalf("unexpected: %+v", u)
		}
		if u.LogicalCallCount != 5 || u.SuccessCount != 4 || u.FailureCount != 1 {
			t.Fatalf("counts not preserved: %+v", u)
		}
	})
}

// ---- service composition / failure authority ----------------------------

func TestGetRunReportRunNotFound(t *testing.T) {
	svc := &evaluationReportService{
		runRepository: &reportFakeRunRepository{err: repository.ErrEvaluationRunNotFound},
		usageService:  &reportFakeUsageService{},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	_, err := svc.GetRunReport(ctx, "run-id")
	if err == nil {
		t.Fatalf("expected not-found error to propagate")
	}
}

func TestGetRunReportObservationDegradesOnAggregateFailure(t *testing.T) {
	svc := &evaluationReportService{
		runRepository: &reportFakeRunRepository{run: completedRun()},
		usageService:  &reportFakeUsageService{aggErr: context.DeadlineExceeded},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	report, err := svc.GetRunReport(ctx, completedRun().RunID)
	if err != nil {
		t.Fatalf("run control fact must not fail on observation error: %v", err)
	}
	if report.Usage.Availability != types.AvailabilityUnknown {
		t.Fatalf("usage must degrade to UNKNOWN: %+v", report.Usage)
	}
	if report.Cost.Availability != types.AvailabilityUnknown {
		t.Fatalf("cost must degrade to UNKNOWN: %+v", report.Cost)
	}
	if report.Quality.Availability != types.AvailabilityAvailable {
		t.Fatalf("quality must still be available: %+v", report.Quality)
	}
	if len(report.Warnings) == 0 {
		t.Fatalf("expected a warning for degraded observation")
	}
}

func TestGetRunReportFullComposition(t *testing.T) {
	agg := &types.ModelUsageAggregate{
		LogicalCallCount: 5, SuccessCount: 5,
		KnownCostTotal: floatPtr(1.25), Currency: strPtr("CNY"),
		CacheEligibleCount: 5, CacheReportedCount: 5,
		LocalEmbeddingCache: &types.EmbeddingCacheAggregate{
			ImplementationStatus: types.EmbeddingCacheImplementationEnabled,
			MeasurementStatus:    types.MeasurementHealthComplete,
			HitCount:             3, MissCount: 2,
		},
	}
	health := &types.MeasurementHealth{
		TenantID: 1, From: time.Now().Add(-10 * time.Second), To: time.Now(),
		Status: types.MeasurementHealthComplete,
	}
	svc := newReportService(completedRun(), agg, health)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	report, err := svc.GetRunReport(ctx, completedRun().RunID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.SchemaVersion != "evaluation-run-report/v1" {
		t.Fatalf("schema version: %s", report.SchemaVersion)
	}
	if report.Run.RunID != completedRun().RunID || report.Run.LegacyTaskID != completedRun().TaskID {
		t.Fatalf("run identity wrong: %+v", report.Run)
	}
	if report.Cost.Availability != types.AvailabilityAvailable {
		t.Fatalf("cost: %+v", report.Cost)
	}
	if !report.SupportingObservation.TenantWindowHealthIsNotRunScope {
		t.Fatalf("tenant-window health must be explicitly marked not-run-scope")
	}
	if report.SupportingObservation.RunMeasurementStatus != string(types.MeasurementStatusUnknown) {
		t.Fatalf("run measurement status must stay UNKNOWN")
	}
	if report.SupportingObservation.LocalEmbeddingCache == nil {
		t.Fatalf("expected local embedding cache supporting fact")
	}
}
