package pricing

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// BenchmarkEstimate measures the fixed-point estimator hot path. Task012 §11
// requires no runtime network/DB lookup and a p95 <= 1 ms on the reference
// environment. The estimator is a pure in-memory function; this benchmark
// confirms it stays well under the target.
func BenchmarkEstimate(b *testing.B) {
	w := validWire()
	buf, err := json.Marshal(w)
	if err != nil {
		b.Fatal(err)
	}
	c, err := LoadCatalog(buf)
	if err != nil {
		b.Fatal(err)
	}
	e := NewEstimator(c)
	e.now = func() time.Time { return time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC) }
	call := &types.ModelCall{
		ModelName:     "Qwen/Qwen3-14B",
		Provider:      "siliconflow",
		Operation:     types.ModelOperationChat,
		Success:       true,
		UsageFinality: types.UsageFinalityReported,
		InputTokens:   intp(100),
		OutputTokens:  intp(50),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = e.Estimate(call)
	}
}
