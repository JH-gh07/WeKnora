package repository

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// BenchmarkModelCallMeteringFullPath measures the full Gorm repository metering
// path — BeginModelCall (durable STARTED marker INSERT) followed by
// RecordModelCall (call INSERT + marker finalize UPDATE in one transaction) —
// against a real SQLite-backed repository. This complements the direct-SQL
// micro-benchmark used for OFF/ON overhead by covering the Gorm ORM + wrapper
// integration, not just raw SQL.
func BenchmarkModelCallMeteringFullPath(b *testing.B) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(b.TempDir(), "bench.db")), &gorm.Config{})
	if err != nil {
		b.Fatal(err)
	}
	if err := db.AutoMigrate(&types.ModelCall{}); err != nil {
		b.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE model_metering_health (id VARCHAR(36) PRIMARY KEY, tenant_id INTEGER NOT NULL, attempted_at DATETIME NOT NULL, persisted BOOLEAN NOT NULL)`).Error; err != nil {
		b.Fatal(err)
	}
	repo := NewModelCallRepository(db)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now := time.Now()
		healthID, err := repo.BeginModelCall(ctx, 1, now)
		if err != nil {
			b.Fatal(err)
		}
		call := &types.ModelCall{
			TenantID:             1,
			ModelID:              "bench-chat",
			ModelName:            "bench",
			Provider:             "bench",
			Operation:            types.ModelOperationChat,
			UsageFinality:        types.UsageFinalityUnavailable,
			CacheStatus:          types.PromptCacheStatusUnreported,
			Success:              true,
			AttemptObservability: types.AttemptObservabilityUnobservable,
			CreatedAt:            now,
		}
		if err := repo.RecordModelCall(ctx, healthID, call); err != nil {
			b.Fatal(err)
		}
	}
}
