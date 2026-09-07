// Command live_probe exercises the real Task012 pricing stack against the real
// SiliconFlow provider: chat adapter -> metering wrapper -> pricing recorder ->
// SQLite repository. It prints only sanitized facts (no prompt/response/key).
//
// Usage:
//
//	SILICONFLOW_API_KEY=... SILICONFLOW_API_URL=https://api.siliconflow.cn/v1 \
//	  go run ./scripts/tmpCheck/task012/live_probe
package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/models/pricing"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	frozenInputNanos  int64 = 500_000_000 // 0.5 CNY per 1M tokens
	frozenOutputNanos int64 = 2_000_000_000
)

func main() {
	apiKey := os.Getenv("SILICONFLOW_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "FATAL: SILICONFLOW_API_KEY not set")
		os.Exit(2)
	}
	baseURL := os.Getenv("SILICONFLOW_API_URL")
	if baseURL == "" {
		baseURL = "https://api.siliconflow.cn/v1"
	}

	dbPath := filepath.Join(os.TempDir(), "task012-live-probe.db")
	_ = os.Remove(dbPath)
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	must(err)
	must(db.AutoMigrate(&types.ModelCall{}))
	must(db.Exec(`CREATE TABLE model_metering_health (id VARCHAR(36) PRIMARY KEY, tenant_id INTEGER NOT NULL, attempted_at DATETIME NOT NULL, persisted BOOLEAN NOT NULL)`).Error)

	repo := repository.NewModelCallRepository(db)
	catalog, err := pricing.LoadDefaultCatalog()
	must(err)
	estimator := pricing.NewEstimator(catalog)
	recorder := pricing.NewPricingRecorder(repo, estimator)

	model := &types.Model{
		ID:     "sf-qwen3-14b",
		Name:   "Qwen/Qwen3-14B",
		Source: types.ModelSourceRemote,
		Type:   types.ModelTypeKnowledgeQA,
		Parameters: types.ModelParameters{
			BaseURL:  baseURL,
			APIKey:   apiKey,
			Provider: "siliconflow",
		},
	}
	cfg := chat.ConfigFromModel(model, "", "")
	cfg.Recorder = recorder
	c, err := chat.NewChat(cfg, nil)
	must(err)

	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	ctx = types.WithLLMCallScope(ctx, "run-live", "", "trace-live")
	ctx = types.WithLLMCallMetadata(ctx, "task012_live_probe", "")

	msg := []chat.Message{{Role: "user", Content: "Reply with exactly: OK"}}

	run := func(stream bool, label string) {
		if stream {
			ch, err := c.ChatStream(ctx, msg, &chat.ChatOptions{MaxCompletionTokens: 32})
			if err != nil {
				fmt.Printf("%s\tstream\tERROR\t%v\n", label, sanitize(err))
				return
			}
			var n int
			for range ch {
				n++
			}
			fmt.Printf("%s\tstream\tOK\tchunks=%d\n", label, n)
			return
		}
		resp, err := c.Chat(ctx, msg, &chat.ChatOptions{MaxCompletionTokens: 32})
		if err != nil {
			fmt.Printf("%s\tnonstream\tERROR\t%v\n", label, sanitize(err))
			return
		}
		fmt.Printf("%s\tnonstream\tOK\tusage_in=%d usage_out=%d\n", label, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	}

	run(false, "P1")
	run(false, "P2")
	run(true, "P3")
	run(true, "P4")

	// Give the detached metering writes a moment to land, then read back.
	time.Sleep(500 * time.Millisecond)

	rows, err := db.Raw(`SELECT id, model_name, provider, operation, success, usage_finality, input_tokens, output_tokens, pricing_status, pricing_reason, pricing_rule_id, pricing_catalog_hash, pricing_unit, input_unit_price_nanos_per_million, output_unit_price_nanos_per_million, estimated_cost_nanos, estimated_cost, currency, pricing_version FROM model_calls WHERE tenant_id = 1 ORDER BY created_at ASC`).Rows()
	must(err)
	defer rows.Close()

	fmt.Println("---ROWS---")
	var mismatches int
	for rows.Next() {
		var id, modelName, provider, operation, finality, pStatus, pReason, ruleID, hash, unit, currency, version string
		var success bool
		var inTok, outTok sql.NullInt64
		var inNanos, outNanos, costNanos sql.NullInt64
		var costFloat sql.NullFloat64
		must(rows.Scan(&id, &modelName, &provider, &operation, &success, &finality, &inTok, &outTok, &pStatus, &pReason, &ruleID, &hash, &unit, &inNanos, &outNanos, &costNanos, &costFloat, &currency, &version))
		fmt.Printf("id=%s model=%s provider=%s op=%s success=%v finality=%s in=%d out=%d status=%s reason=%s rule=%s hash=%s unit=%s in_nanos=%d out_nanos=%d cost_nanos=%d cost_dec=%g currency=%s version=%s\n",
			id, modelName, provider, operation, success, finality, inTok.Int64, outTok.Int64, pStatus, pReason, ruleID, hash, unit, inNanos.Int64, outNanos.Int64, costNanos.Int64, costFloat.Float64, currency, version)

		if pStatus == "PRICED" && inTok.Valid && outTok.Valid {
			want := referenceNanos(inTok.Int64, outTok.Int64)
			status := "MATCH"
			if costNanos.Int64 != want {
				status = "MISMATCH"
				mismatches++
			}
			fmt.Printf("  recompute: want_nanos=%d got_nanos=%d -> %s\n", want, costNanos.Int64, status)
		}
	}
	fmt.Printf("---SUMMARY mismatches=%d---\n", mismatches)
	if mismatches > 0 {
		os.Exit(1)
	}
}

// referenceNanos is an independent fixed-point recomputation using math/big,
// mirroring the plan formula but not sharing code with the estimator.
func referenceNanos(input, output int64) int64 {
	raw := new(big.Int).Mul(big.NewInt(input), big.NewInt(frozenInputNanos))
	raw.Add(raw, new(big.Int).Mul(big.NewInt(output), big.NewInt(frozenOutputNanos)))
	den := big.NewInt(1_000_000)
	q, r := new(big.Int).QuoRem(raw, den, new(big.Int))
	if new(big.Int).Mul(r, big.NewInt(2)).Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64()
}

func sanitize(err error) string {
	// Do not leak provider error bodies into evidence.
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%T", err)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
