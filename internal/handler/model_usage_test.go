package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubModelUsageService implements ModelUsageService for handler tests.
// Embedding the interface surfaces contract drift loudly (panic) instead of
// silently returning zero values.
type stubModelUsageService struct {
	interfaces.ModelUsageService
	aggregate func(ctx context.Context, filter types.ModelCallFilter) (*types.ModelUsageAggregate, error)
	health    func(ctx context.Context, tenantID uint64, from, to time.Time) (*types.MeasurementHealth, error)
}

func (s *stubModelUsageService) Aggregate(ctx context.Context, filter types.ModelCallFilter) (*types.ModelUsageAggregate, error) {
	return s.aggregate(ctx, filter)
}

func (s *stubModelUsageService) Health(ctx context.Context, tenantID uint64, from, to time.Time) (*types.MeasurementHealth, error) {
	return s.health(ctx, tenantID, from, to)
}

func newModelUsageTestRouter(svc interfaces.ModelUsageService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(func(c *gin.Context) {
		c.Set(types.TenantIDContextKey.String(), uint64(7))
		c.Next()
	})
	h := NewModelUsageHandler(svc)
	r.GET("/model-usage", h.Aggregate)
	r.GET("/model-usage/health", h.Health)
	return r
}

func TestModelUsageAggregate_OkTenantFromContextNotQuery(t *testing.T) {
	// A tenant query param must NOT override the authenticated tenant.
	var gotFilter types.ModelCallFilter
	svc := &stubModelUsageService{
		aggregate: func(_ context.Context, f types.ModelCallFilter) (*types.ModelUsageAggregate, error) {
			gotFilter = f
			return &types.ModelUsageAggregate{
				LogicalCallCount: 3, MeasurementStatus: types.MeasurementHealthComplete,
			}, nil
		},
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/model-usage?tenant=999&model_id=deepseek-v3&operation=chat&purpose=evaluation&run_id=run-123"+
			"&from=2026-08-24T00:00:00Z&to=2026-08-24T01:00:00Z", nil)
	newModelUsageTestRouter(svc).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if gotFilter.TenantID != 7 {
		t.Fatalf("expected tenant 7 from context, got %d (query tenant must be ignored)", gotFilter.TenantID)
	}
	if gotFilter.ModelID != "deepseek-v3" || gotFilter.Operation != types.ModelOperationChat ||
		gotFilter.Purpose != "evaluation" {
		t.Fatalf("unexpected filter passthrough: %+v", gotFilter)
	}
	if gotFilter.RunID == nil || *gotFilter.RunID != "run-123" {
		t.Fatalf("expected run_id=run-123, got %v", gotFilter.RunID)
	}
	if gotFilter.From == nil || gotFilter.To == nil {
		t.Fatalf("expected From/To to be parsed, got %v %v", gotFilter.From, gotFilter.To)
	}
	var body struct {
		Success bool                      `json:"success"`
		Data    types.ModelUsageAggregate `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !body.Success || body.Data.LogicalCallCount != 3 {
		t.Fatalf("unexpected envelope: %s", w.Body.String())
	}
}

func TestModelUsageAggregate_EmptyRunIDIsNil(t *testing.T) {
	var gotFilter types.ModelCallFilter
	svc := &stubModelUsageService{
		aggregate: func(_ context.Context, f types.ModelCallFilter) (*types.ModelUsageAggregate, error) {
			gotFilter = f
			return &types.ModelUsageAggregate{}, nil
		},
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/model-usage?run_id=%20%20", nil)
	newModelUsageTestRouter(svc).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if gotFilter.RunID != nil {
		t.Fatalf("whitespace run_id must map to nil filter, got %v", *gotFilter.RunID)
	}
}

func TestModelUsageAggregate_BadWindow(t *testing.T) {
	svc := &stubModelUsageService{
		aggregate: func(_ context.Context, _ types.ModelCallFilter) (*types.ModelUsageAggregate, error) {
			t.Fatal("service must not be called for a bad window")
			return nil, nil
		},
	}
	for _, q := range []string{
		"from=not-a-time",
		"to=also-bad",
		"from=2026-08-24T02:00:00Z&to=2026-08-24T01:00:00Z", // from >= to
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/model-usage?"+q, nil)
		newModelUsageTestRouter(svc).ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("query %q: expected 400, got %d body=%s", q, w.Code, w.Body.String())
		}
	}
}

func TestModelUsageAggregate_BadOperation(t *testing.T) {
	svc := &stubModelUsageService{
		aggregate: func(_ context.Context, _ types.ModelCallFilter) (*types.ModelUsageAggregate, error) {
			t.Fatal("service must not be called for a bad operation")
			return nil, nil
		},
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/model-usage?operation=embed", nil)
	newModelUsageTestRouter(svc).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown operation, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestModelUsageHealth_Ok(t *testing.T) {
	var gotTenant uint64
	svc := &stubModelUsageService{
		health: func(_ context.Context, tenantID uint64, from, to time.Time) (*types.MeasurementHealth, error) {
			gotTenant = tenantID
			return &types.MeasurementHealth{
				TenantID: tenantID, From: from, To: to,
				Status: types.MeasurementHealthUnknown,
			}, nil
		},
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/model-usage/health?from=2026-08-24T00:00:00Z&to=2026-08-24T01:00:00Z", nil)
	newModelUsageTestRouter(svc).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if gotTenant != 7 {
		t.Fatalf("expected tenant 7 from context, got %d", gotTenant)
	}
	var body struct {
		Success bool                    `json:"success"`
		Data    types.MeasurementHealth `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !body.Success || body.Data.Status != types.MeasurementHealthUnknown {
		t.Fatalf("unexpected envelope: %s", w.Body.String())
	}
}

func TestModelUsageHealth_NoTenantUnauthorized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	h := NewModelUsageHandler(&stubModelUsageService{})
	r.GET("/model-usage/health", h.Health)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/model-usage/health", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without tenant context, got %d body=%s", w.Code, w.Body.String())
	}
}
