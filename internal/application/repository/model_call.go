package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var ErrModelCallNotFound = errors.New("model call not found")
var ErrModelCallTenantRequired = errors.New("model call tenant is required")
var ErrModelCallHealthRequired = errors.New("model call health id is required")
var ErrModelCallHealthNotFound = errors.New("model call health marker not found")

// meteringWriteTimeout bounds the detached metering write so a hung database
// cannot hold a goroutine forever after the business request has returned.
const meteringWriteTimeout = 5 * time.Second

// detachContext removes cancellation/deadline propagation from the business
// request (the provider already ran and cost money — the metering write must
// not be dropped because the client disconnected) while keeping its values and
// adding a short hard deadline. The returned cancel stops the deadline timer.
func detachContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), meteringWriteTimeout)
}

type modelCallRepository struct{ db *gorm.DB }

func NewModelCallRepository(db *gorm.DB) interfaces.ModelCallRepository {
	return &modelCallRepository{db: db}
}

func (r *modelCallRepository) CreateModelCall(ctx context.Context, call *types.ModelCall) error {
	if call == nil || call.TenantID == 0 {
		return ErrModelCallTenantRequired
	}
	if call.ID == "" {
		call.ID = uuid.NewString()
	}
	if call.CreatedAt.IsZero() {
		call.CreatedAt = time.Now().UTC()
	}
	return r.db.WithContext(ctx).Create(call).Error
}

// BeginModelCall writes the durable "started, not yet persisted" health marker
// before the provider round-trip. It returns the marker id used to finalize the
// attempt in RecordModelCall. The write is detached from the business context.
func (r *modelCallRepository) BeginModelCall(ctx context.Context, tenantID uint64, at time.Time) (string, error) {
	if tenantID == 0 {
		return "", ErrModelCallTenantRequired
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	id := uuid.NewString()
	dctx, cancel := detachContext(ctx)
	defer cancel()
	err := r.db.WithContext(dctx).Exec(
		`INSERT INTO model_metering_health (id, tenant_id, attempted_at, persisted) VALUES (?, ?, ?, ?)`,
		id, tenantID, at.UTC(), false,
	).Error
	if err != nil {
		return "", err
	}
	return id, nil
}

// RecordModelCall persists the call fact and finalizes the started health
// marker as persisted in one transaction. If the transaction fails the marker
// remains not-persisted, so the queried window counts the attempt as failed
// (PARTIAL) rather than silently COMPLETE. When no marker exists (BeginModelCall
// failed because the DB was down at call start) it falls back to a single
// best-effort transaction so a call that completes after DB recovery is not
// silently dropped. The write is detached from the business context.
func (r *modelCallRepository) RecordModelCall(ctx context.Context, healthID string, call *types.ModelCall) error {
	if call == nil || call.TenantID == 0 {
		return ErrModelCallTenantRequired
	}
	at := call.CreatedAt
	if at.IsZero() {
		at = time.Now().UTC()
		call.CreatedAt = at
	}
	dctx, cancel := detachContext(ctx)
	defer cancel()
	if healthID != "" {
		err := r.db.WithContext(dctx).Transaction(func(tx *gorm.DB) error {
			txRepo := &modelCallRepository{db: tx}
			if err := txRepo.CreateModelCall(dctx, call); err != nil {
				return err
			}
			return txRepo.finalizeModelCallHealth(dctx, healthID, call.TenantID, true)
		})
		if err != nil {
			// Marker already persists as not-persisted; window degrades to PARTIAL.
			return err
		}
		return nil
	}
	// Fallback without a marker (DB was down at call start): one best-effort
	// transaction, then a best-effort failed health event on failure.
	err := r.db.WithContext(dctx).Transaction(func(tx *gorm.DB) error {
		txRepo := &modelCallRepository{db: tx}
		if err := txRepo.CreateModelCall(dctx, call); err != nil {
			return err
		}
		return txRepo.RecordMeteringAttempt(dctx, call.TenantID, at, true)
	})
	if err != nil {
		if healthErr := r.RecordMeteringAttempt(dctx, call.TenantID, at, false); healthErr != nil {
			return errors.Join(err, healthErr)
		}
		return err
	}
	return nil
}

// finalizeModelCallHealth transitions a started marker to its final persisted
// state. It is fail-closed: a missing marker rolls back the call insert.
func (r *modelCallRepository) finalizeModelCallHealth(ctx context.Context, healthID string, tenantID uint64, persisted bool) error {
	res := r.db.WithContext(ctx).Exec(
		`UPDATE model_metering_health SET persisted = ? WHERE id = ? AND tenant_id = ?`,
		persisted, healthID, tenantID,
	)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrModelCallHealthNotFound
	}
	return nil
}

func (r *modelCallRepository) GetModelCall(ctx context.Context, tenantID uint64, id string) (*types.ModelCall, error) {
	var call types.ModelCall
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).First(&call).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrModelCallNotFound
	}
	return &call, err
}

func (r *modelCallRepository) AggregateModelCalls(ctx context.Context, filter types.ModelCallFilter) (*types.ModelUsageAggregate, error) {
	q := r.db.WithContext(ctx).Model(&types.ModelCall{}).Where("tenant_id = ?", filter.TenantID)
	if filter.RunID != nil {
		q = q.Where("run_id = ?", *filter.RunID)
	}
	if filter.ModelID != "" {
		q = q.Where("model_id = ?", filter.ModelID)
	}
	if filter.Operation != "" {
		q = q.Where("operation = ?", filter.Operation)
	}
	if filter.Purpose != "" {
		q = q.Where("purpose = ?", filter.Purpose)
	}
	if filter.From != nil {
		q = q.Where("created_at >= ?", *filter.From)
	}
	if filter.To != nil {
		q = q.Where("created_at < ?", *filter.To)
	}
	var row struct {
		LogicalCallCount                                                                                        int64
		SuccessCount                                                                                            int64
		FailureCount                                                                                            int64
		InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens, CacheMissTokens, CacheReportedInputTokens sql.NullInt64
		KnownCostTotal                                                                                          sql.NullFloat64
		Currency                                                                                                sql.NullString
		KnownCostCurrencyCount, KnownCostMissingCurrencyCount                                                   int64
		UnknownCostCallCount                                                                                    int64
		CacheEligibleCount, CacheReportedCount, CacheUnsupportedCount                                           int64
	}
	// SUM remains NULL when every value is unknown. This is deliberate: the
	// caller can distinguish unknown telemetry from an observed zero.
	err := q.Select(`COUNT(*) AS logical_call_count,
		SUM(CASE WHEN success THEN 1 ELSE 0 END) AS success_count,
		SUM(CASE WHEN NOT success THEN 1 ELSE 0 END) AS failure_count,
		SUM(input_tokens) AS input_tokens, SUM(output_tokens) AS output_tokens,
		SUM(cache_read_tokens) AS cache_read_tokens, SUM(cache_write_tokens) AS cache_write_tokens,
		SUM(cache_miss_tokens) AS cache_miss_tokens,
		SUM(cache_reported_input_tokens) AS cache_reported_input_tokens,
		SUM(estimated_cost) AS known_cost_total,
		MIN(CASE WHEN estimated_cost IS NOT NULL AND currency <> '' THEN currency END) AS currency,
		COUNT(DISTINCT CASE WHEN estimated_cost IS NOT NULL AND currency <> '' THEN currency END) AS known_cost_currency_count,
		SUM(CASE WHEN estimated_cost IS NOT NULL AND currency = '' THEN 1 ELSE 0 END) AS known_cost_missing_currency_count,
		SUM(CASE WHEN estimated_cost IS NULL THEN 1 ELSE 0 END) AS unknown_cost_call_count,
		SUM(CASE WHEN cache_status <> 'unsupported' THEN 1 ELSE 0 END) AS cache_eligible_count,
		SUM(CASE WHEN cache_status IN ('hit','miss') THEN 1 ELSE 0 END) AS cache_reported_count,
		SUM(CASE WHEN cache_status = 'unsupported' THEN 1 ELSE 0 END) AS cache_unsupported_count`).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	out := &types.ModelUsageAggregate{LogicalCallCount: row.LogicalCallCount, SuccessCount: row.SuccessCount, FailureCount: row.FailureCount,
		UnknownCostCallCount: row.UnknownCostCallCount, CacheEligibleCount: row.CacheEligibleCount, CacheReportedCount: row.CacheReportedCount, CacheUnsupportedCount: row.CacheUnsupportedCount}
	assignInt := func(v sql.NullInt64) *int64 {
		if !v.Valid {
			return nil
		}
		x := v.Int64
		return &x
	}
	out.InputTokens, out.OutputTokens, out.CacheReadTokens = assignInt(row.InputTokens), assignInt(row.OutputTokens), assignInt(row.CacheReadTokens)
	out.CacheWriteTokens, out.CacheMissTokens = assignInt(row.CacheWriteTokens), assignInt(row.CacheMissTokens)
	out.CacheReportedInputTokens = assignInt(row.CacheReportedInputTokens)
	// A subtotal is only a usable fact when every priced row has the same,
	// non-empty historical currency identity. Mixed or unidentified currencies
	// fail closed instead of returning a dimensionless/mixed sum.
	out.MixedCurrency = row.KnownCostCurrencyCount > 1
	if row.KnownCostTotal.Valid && row.KnownCostCurrencyCount == 1 && row.KnownCostMissingCurrencyCount == 0 && row.Currency.Valid {
		x := row.KnownCostTotal.Float64
		out.KnownCostTotal = &x
		currency := row.Currency.String
		out.Currency = &currency
	}
	health, err := r.GetMeasurementHealth(ctx, filter.TenantID, valueOrMin(filter.From), valueOrMax(filter.To))
	if err != nil {
		return nil, err
	}
	out.MeteringAttemptedCount, out.MeteringPersistedCount, out.MeteringFailedCount = health.MeteringAttemptedCount, health.MeteringPersistedCount, health.MeteringFailedCount
	out.MeasurementStatus = health.Status
	return out, nil
}

func valueOrMin(v *time.Time) time.Time {
	if v != nil {
		return *v
	}
	return time.Unix(0, 0).UTC()
}
func valueOrMax(v *time.Time) time.Time {
	if v != nil {
		return *v
	}
	return time.Now().UTC().Add(100 * 365 * 24 * time.Hour)
}

func (r *modelCallRepository) RecordMeteringAttempt(ctx context.Context, tenantID uint64, at time.Time, persisted bool) error {
	return r.db.WithContext(ctx).Exec(`INSERT INTO model_metering_health (id, tenant_id, attempted_at, persisted) VALUES (?, ?, ?, ?)`, uuid.NewString(), tenantID, at.UTC(), persisted).Error
}

func (r *modelCallRepository) GetMeasurementHealth(ctx context.Context, tenantID uint64, from, to time.Time) (*types.MeasurementHealth, error) {
	var row struct{ Attempted, Persisted, Failed int64 }
	err := r.db.WithContext(ctx).Table("model_metering_health").Select("COUNT(*) AS attempted, COALESCE(SUM(CASE WHEN persisted THEN 1 ELSE 0 END),0) AS persisted, COALESCE(SUM(CASE WHEN NOT persisted THEN 1 ELSE 0 END),0) AS failed").Where("tenant_id = ? AND attempted_at >= ? AND attempted_at < ?", tenantID, from, to).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	status := types.MeasurementHealthComplete
	if row.Failed > 0 {
		status = types.MeasurementHealthPartial
	}
	if row.Attempted == 0 {
		status = types.MeasurementHealthUnknown
	}
	return &types.MeasurementHealth{TenantID: tenantID, From: from, To: to, MeteringAttemptedCount: row.Attempted, MeteringPersistedCount: row.Persisted, MeteringFailedCount: row.Failed, Status: status}, nil
}
