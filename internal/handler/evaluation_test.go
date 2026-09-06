package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type stubReportService struct {
	interfaces.EvaluationReportService
	report *types.EvaluationRunReport
	err    error
}

func (s *stubReportService) GetRunReport(_ context.Context, _ string) (*types.EvaluationRunReport, error) {
	return s.report, s.err
}

func newEvaluationReportTestRouter(svc interfaces.EvaluationReportService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(func(c *gin.Context) {
		c.Set(types.TenantIDContextKey.String(), uint64(7))
		c.Next()
	})
	h := NewEvaluationHandler(nil, svc)
	r.GET("/evaluation/runs/:run_id/report", h.GetEvaluationRunReport)
	return r
}

func TestGetEvaluationRunReportOk(t *testing.T) {
	svc := &stubReportService{report: &types.EvaluationRunReport{SchemaVersion: "evaluation-run-report/v1"}}
	r := newEvaluationReportTestRouter(svc)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/evaluation/runs/00000000-0000-0000-0000-000000000001/report", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestGetEvaluationRunReportMalformedRunID(t *testing.T) {
	r := newEvaluationReportTestRouter(&stubReportService{})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/evaluation/runs/not-a-uuid/report", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestGetEvaluationRunReportNotFound(t *testing.T) {
	svc := &stubReportService{err: repository.ErrEvaluationRunNotFound}
	r := newEvaluationReportTestRouter(svc)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/evaluation/runs/00000000-0000-0000-0000-000000000001/report", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestGetEvaluationRunReportInternalErrorIsRedacted(t *testing.T) {
	const sensitive = "dial postgres: password=do-not-return"
	svc := &stubReportService{err: errors.New(sensitive)}
	r := newEvaluationReportTestRouter(svc)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/evaluation/runs/00000000-0000-0000-0000-000000000001/report", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Message == sensitive {
		t.Fatalf("internal error leaked to client: %q", body.Error.Message)
	}
}
