// Command live_http starts the REAL gin router + report handler + repository +
// service over a real TCP listener, seeds canonical fixtures, and then issues
// real HTTP GETs (network round-trips) against the live endpoint to produce the
// sanitized report samples and the A03–A06/A08–A15 HTTP matrix (Step 8).
//
// It prints NO prompt/document/model-response bodies and NO credentials.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const tenantID uint64 = 42

var (
	t0    = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	t1    = t0.Add(10 * time.Second)
	metri = types.JSON(`{"retrieval_metrics":{"precision":0.85,"recall":0.92,"ndcg3":0.88,"ndcg10":0.86,"mrr":0.95,"map":0.87},"generation_metrics":{"bleu1":0.72,"bleu2":0.65,"bleu4":0.58,"rouge1":0.78,"rouge2":0.71,"rougel":0.75}}`)
)

func main() {
	serveMode := len(os.Args) > 1 && os.Args[1] == "--serve"
	argOffset := 1
	if serveMode {
		argOffset = 2
	}
	outDir := ""
	if len(os.Args) > argOffset {
		outDir = os.Args[argOffset]
	}
	if outDir == "" {
		outDir = "/tmp/task010-livehttp"
	}
	os.MkdirAll(filepath.Join(outDir, "sanitized_reports"), 0o755)

	db, err := gorm.Open(sqlite.Open(filepath.Join(outDir, "live.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		fatal("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&types.EvaluationRun{}, &types.ModelCall{}, &types.EmbeddingCacheObservation{}); err != nil {
		fatal("automigrate: %v", err)
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS model_metering_health (id VARCHAR(36) PRIMARY KEY, tenant_id INTEGER NOT NULL, attempted_at DATETIME NOT NULL, persisted BOOLEAN NOT NULL)`).Error; err != nil {
		fatal("health table: %v", err)
	}

	seed(db)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	runRepo := repository.NewEvaluationRunRepository(db)
	callRepo := repository.NewModelCallRepository(db)
	cacheRepo := repository.NewEmbeddingCacheRepository(db)
	usageSvc := service.NewModelUsageService(callRepo, cacheRepo, nil)
	reportSvc := service.NewEvaluationReportService(runRepo, usageSvc)
	h := handler.NewEvaluationHandler(nil, reportSvc)
	r.Use(func(c *gin.Context) {
		activeTenantID := tenantID
		if raw := c.GetHeader("X-Tenant-ID"); raw != "" {
			if parsed, parseErr := strconv.ParseUint(raw, 10, 64); parseErr == nil && parsed > 0 {
				activeTenantID = parsed
			}
		}
		c.Set(types.TenantIDContextKey.String(), activeTenantID)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), types.TenantIDContextKey, activeTenantID))
		c.Next()
	})
	var pollingRequests atomic.Int32
	reportHandler := func(c *gin.Context) {
		runID := c.Param("run_id")
		switch runID {
		case "00000000-0000-0000-0000-00000000b002":
			// Leave enough time for the browser to observe the loading state
			// deterministically, including on a busy CI host.
			time.Sleep(1500 * time.Millisecond)
		case "00000000-0000-0000-0000-00000000a014":
			time.Sleep(800 * time.Millisecond)
		case "00000000-0000-0000-0000-00000000b004":
			if pollingRequests.Add(1) >= 2 {
				_ = db.Model(&types.EvaluationRun{}).
					Where("run_id = ?", runID).
					Updates(map[string]any{
						"status":        types.EvaluationRunStatusCompleted,
						"ended_at":      t1,
						"metrics_json":  metri,
						"metrics_valid": true,
					}).Error
			}
		}
		h.GetEvaluationRunReport(c)
	}
	r.GET("/evaluation/runs/:run_id/report", reportHandler)
	r.GET("/api/v1/evaluation/runs/:run_id/report", reportHandler)
	r.GET("/api/v1/system/deployment-capabilities", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"edition": "community", "capabilities": gin.H{}}})
	})
	// The browser fixture only exercises the real report path. Layout-level
	// optional reads receive an empty success envelope so unrelated navigation
	// data cannot make the report matrix flaky.
	r.NoRoute(func(c *gin.Context) {
		// Platform chrome prefetches list resources (sessions, agents, KBs)
		// while the report is open. Preserve their common empty-list contract
		// so the fixture does not manufacture unrelated Vue exceptions.
		c.JSON(http.StatusOK, gin.H{"success": true, "data": []any{}, "total": 0})
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("listen: %v", err)
	}
	srv := &http.Server{Handler: r}
	go srv.Serve(ln)
	base := "http://" + ln.Addr().String()
	if serveMode {
		if len(os.Args) <= argOffset+1 {
			fatal("--serve requires an address-file path")
		}
		if err := os.WriteFile(os.Args[argOffset+1], []byte(base), 0o600); err != nil {
			fatal("write address file: %v", err)
		}
		fmt.Printf("task010 browser fixture listening at %s\n", base)
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		_ = srv.Close()
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	type scenario struct {
		name   string
		runID  string
		expect int
	}
	cases := []scenario{
		{"completed_known_cost", "00000000-0000-0000-0000-0000000000a1", 200},
		{"completed_unknown_cost", "00000000-0000-0000-0000-0000000000b2", 200},
		{"partial_measurement", "00000000-0000-0000-0000-0000000000c3", 200},
		{"running", "00000000-0000-0000-0000-0000000000d4", 200},
		{"failed", "00000000-0000-0000-0000-0000000000e5", 200},
		{"interrupted", "00000000-0000-0000-0000-0000000000f6", 200},
		{"cross_tenant", "00000000-0000-0000-0000-0000000000aa", 404},
		{"not_found", "00000000-0000-0000-0000-00000000dead", 404},
		{"malformed", "not-a-uuid", 400},
	}

	fail := 0
	for _, c := range cases {
		resp, err := client.Get(base + "/evaluation/runs/" + c.runID + "/report")
		if err != nil {
			fmt.Printf("[%s] transport error: %v\n", c.name, err)
			fail++
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.expect {
			fmt.Printf("[%s] expected %d got %d body=%s\n", c.name, c.expect, resp.StatusCode, truncate(string(body), 200))
			fail++
			continue
		}
		if c.expect == 200 {
			var envelope struct {
				Success bool                      `json:"success"`
				Data    types.EvaluationRunReport `json:"data"`
			}
			if err := json.Unmarshal(body, &envelope); err != nil || !envelope.Success {
				fmt.Printf("[%s] envelope decode/success fail: %v\n", c.name, err)
				fail++
				continue
			}
			var pretty []byte
			pretty, _ = json.MarshalIndent(envelope.Data, "", "  ")
			os.WriteFile(filepath.Join(outDir, "sanitized_reports", c.name+".json"), pretty, 0o644)
		}
		fmt.Printf("[%s] %d (expect %d)\n", c.name, resp.StatusCode, c.expect)
	}

	srv.Close()
	if fail > 0 {
		fmt.Printf("live-http: %d FAIL\n", fail)
		os.Exit(1)
	}
	fmt.Printf("live-http: PASS (%d cases), reports in %s\n", len(cases), filepath.Join(outDir, "sanitized_reports"))
}

func seed(db *gorm.DB) {
	mk := func(id string, status types.EvaluationRunStatus, metricsValid bool) {
		var started *time.Time
		var ended *time.Time
		var m types.JSON
		if status == types.EvaluationRunStatusRunning {
			started = &t0
		} else {
			started = &t0
			ended = &t1
		}
		if metricsValid {
			m = metri
		}
		db.Create(&types.EvaluationRun{
			RunID:             id,
			TenantID:          tenantID,
			TaskID:            "evaluation-42-default",
			Status:            status,
			StartedAt:         started,
			EndedAt:           ended,
			MetricsJSON:       m,
			MetricsValid:      metricsValid,
			ProtocolHash:      "hash-" + id,
			GitCommit:         "deadbeef",
			AppVersion:        "0.8.0",
			PersistenceStatus: types.PersistenceStatusPersisted,
			CleanupStatus:     types.CleanupStatusDeleteRequested,
			MeasurementStatus: types.MeasurementStatusUnknown,
		})
	}

	mk("00000000-0000-0000-0000-0000000000a1", types.EvaluationRunStatusCompleted, true)
	costA := 0.5
	call(db, "00000000-0000-0000-0000-0000000000a1", &costA, "USD", types.PromptCacheStatusHit)

	mk("00000000-0000-0000-0000-0000000000b2", types.EvaluationRunStatusCompleted, true)
	call(db, "00000000-0000-0000-0000-0000000000b2", nil, "", types.PromptCacheStatusMiss)

	mk("00000000-0000-0000-0000-0000000000c3", types.EvaluationRunStatusCompleted, true)
	costC := 0.5
	call(db, "00000000-0000-0000-0000-0000000000c3", &costC, "USD", types.PromptCacheStatusHit)
	call(db, "00000000-0000-0000-0000-0000000000c3", nil, "", types.PromptCacheStatusMiss)

	mk("00000000-0000-0000-0000-0000000000d4", types.EvaluationRunStatusRunning, false)
	mk("00000000-0000-0000-0000-0000000000e5", types.EvaluationRunStatusFailed, false)
	mk("00000000-0000-0000-0000-0000000000f6", types.EvaluationRunStatusInterrupted, false)
	_ = db.Model(&types.EvaluationRun{}).
		Where("run_id = ?", "00000000-0000-0000-0000-0000000000f6").
		Update("interruption_reason", "PROCESS_LOST").Error

	// Browser-only deterministic states. They still travel through the real
	// repository/service/handler stack; only the input facts are fixtures.
	mk("00000000-0000-0000-0000-00000000b002", types.EvaluationRunStatusCompleted, true)
	mk("00000000-0000-0000-0000-00000000b004", types.EvaluationRunStatusRunning, false)
	mk("00000000-0000-0000-0000-00000000a014", types.EvaluationRunStatusCompleted, true)
	mk("00000000-0000-0000-0000-00000000b014", types.EvaluationRunStatusCompleted, true)

	mk("00000000-0000-0000-0000-000000000009", types.EvaluationRunStatusCompleted, true)
	zero := 0.0
	call(db, "00000000-0000-0000-0000-000000000009", &zero, "CNY", types.PromptCacheStatusHit)

	mk("00000000-0000-0000-0000-000000000010", types.EvaluationRunStatusCompleted, true)
	usd := 1.0
	cny := 7.0
	call(db, "00000000-0000-0000-0000-000000000010", &usd, "USD", types.PromptCacheStatusHit)
	call(db, "00000000-0000-0000-0000-000000000010", &cny, "CNY", types.PromptCacheStatusHit)

	mk("00000000-0000-0000-0000-000000000012", types.EvaluationRunStatusCompleted, true)
	call(db, "00000000-0000-0000-0000-000000000012", nil, "", types.PromptCacheStatusUnsupported)

	_ = db.Model(&types.EvaluationRun{}).
		Where("run_id = ?", "00000000-0000-0000-0000-0000000000c3").
		Update("measurement_status", types.MeasurementStatusPartial).Error
	_ = db.Exec(
		`INSERT INTO model_metering_health (id, tenant_id, attempted_at, persisted) VALUES (?, ?, ?, ?), (?, ?, ?, ?)`,
		uuid.NewString(), tenantID, t0.Add(2*time.Second), true,
		uuid.NewString(), tenantID, t0.Add(3*time.Second), false,
	).Error

	// tenant 43 owns this run; tenant 42 must get 404 (no existence leak).
	db.Create(&types.EvaluationRun{
		RunID:             "00000000-0000-0000-0000-0000000000aa",
		TenantID:          43,
		TaskID:            "evaluation-43-default",
		Status:            types.EvaluationRunStatusCompleted,
		StartedAt:         &t0,
		EndedAt:           &t1,
		MetricsJSON:       metri,
		MetricsValid:      true,
		ProtocolHash:      "hash-aa",
		GitCommit:         "deadbeef",
		AppVersion:        "0.8.0",
		PersistenceStatus: types.PersistenceStatusPersisted,
		CleanupStatus:     types.CleanupStatusDeleteRequested,
		MeasurementStatus: types.MeasurementStatusUnknown,
	})
}

func call(db *gorm.DB, runID string, cost *float64, currency string, cache types.PromptCacheStatus) {
	db.Create(&types.ModelCall{
		ID:                   uuid.NewString(),
		TenantID:             tenantID,
		RunID:                &runID,
		Operation:            types.ModelOperationChat,
		ModelID:              "chat-1",
		ModelName:            "chat",
		Provider:             "fixture",
		UsageFinality:        types.UsageFinalityUnavailable,
		CacheStatus:          cache,
		Success:              true,
		AttemptObservability: types.AttemptObservabilityUnobservable,
		CreatedAt:            t0.Add(1 * time.Second),
		EstimatedCost:        cost,
		Currency:             currency,
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func fatal(f string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(2)
}
