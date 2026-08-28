// Command experiment runs the Task005 deterministic OFF/COLD/WARM embedding
// cache experiment against a local SQLite backend. It emits raw per-sample
// JSON, a summary.tsv, and a correctness.tsv. It never prints raw text
// preimages, full vectors, or credentials.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/golang-migrate/migrate/v4"
	sqlite3migrate "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	warmups   = 5
	rounds    = 3
	samples   = 30
	itemCount = 40
	dimension = 3
)

// detProvider is a deterministic in-process embedding provider with a fixed
// vector algorithm and a provider-bound batch call counter.
type detProvider struct{ calls int }

func vectorFor(text string) []float32 {
	sum := sha256.Sum256([]byte(text))
	return []float32{float32(len(text)), float32(sum[0]) / 255.0, float32(sum[1]) / 255.0}
}

func (d *detProvider) GetModelName() string { return "deterministic-fixture" }
func (d *detProvider) GetModelID() string   { return "fixture-embedding-model" }
func (d *detProvider) GetDimensions() int   { return dimension }

func (d *detProvider) Embed(_ context.Context, text string) ([]float32, error) {
	d.calls++
	return vectorFor(text), nil
}

func (d *detProvider) BatchEmbed(_ context.Context, texts []string) ([][]float32, error) {
	d.calls++
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = vectorFor(t)
	}
	return out, nil
}

func (d *detProvider) BatchEmbedWithPool(ctx context.Context, _ embedding.Embedder, texts []string) ([][]float32, error) {
	return d.BatchEmbed(ctx, texts)
}

type sample struct {
	Round           int    `json:"round"`
	Condition       string `json:"condition"`
	SampleID        int    `json:"sample_id"`
	ElapsedMS       int64  `json:"elapsed_ms"`
	LogicalItem     int64  `json:"logical_item_count"`
	Hit             int64  `json:"local_hit_count"`
	Miss            int64  `json:"local_miss_count"`
	Bypass          int64  `json:"local_bypass_count"`
	LookupFail      int64  `json:"lookup_failed_count"`
	Corruption      int64  `json:"corruption_count"`
	WriteFail       int64  `json:"write_failed_count"`
	Provider        int64  `json:"provider_bound_model_call_count"`
	VectorDigest    string `json:"vector_batch_digest"`
	RetrievalDigest string `json:"retrieval_digest"`
	Status          string `json:"measurement_status"`
}

// workload returns round r's batch list: (warmups+samples) batches of itemCount
// distinct texts. OFF and COLD/WARM share the identical text set per round.
func workload(r int) [][]string {
	out := make([][]string, warmups+samples)
	for b := range out {
		out[b] = make([]string, itemCount)
		for i := range out[b] {
			out[b][i] = fmt.Sprintf("round-%d-batch-%04d-item-%04d", r, b, i)
		}
	}
	return out
}

func digestBatch(vectors [][]float32) string {
	var sb strings.Builder
	for _, v := range vectors {
		for _, x := range v {
			sb.WriteString(fmt.Sprintf("%.9g,", x))
		}
		sb.WriteString(";")
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

// retrievalDigest computes a deterministic nearest-neighbour retrieval over the
// batch corpus under a fixed query set and top-k, then hashes the ranked result.
// It is the executable "retrieval non-regression" fact: identical vectors and an
// identical retrieval config must yield an identical digest.
func retrievalDigest(vectors [][]float32) string {
	const k = 3
	queries := []string{"retrieval-query-0", "retrieval-query-1", "retrieval-query-2", "retrieval-query-3"}
	type scored struct {
		idx int
		sim float32
	}
	var sb strings.Builder
	for _, q := range queries {
		qv := vectorFor(q)
		scores := make([]scored, len(vectors))
		for i, v := range vectors {
			scores[i] = scored{idx: i, sim: cosineSimilarity(qv, v)}
		}
		sort.SliceStable(scores, func(a, b int) bool { return scores[a].sim > scores[b].sim })
		for j := 0; j < k && j < len(scores); j++ {
			sb.WriteString(fmt.Sprintf("%d:%d,", j, scores[j].idx))
		}
		sb.WriteString(";")
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

func cosineSimilarity(a, b []float32) float32 {
	var dot, na, nb float32
	for i := range a {
		if i < len(b) {
			dot += a[i] * b[i]
		}
		na += a[i] * a[i]
	}
	for _, x := range b {
		nb += x * x
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / float32(math.Sqrt(float64(na))*math.Sqrt(float64(nb)))
}

type cumulative struct {
	hit, miss, bypass, lookupFail, corruption, writeFail, provider, items int64
	status                                                                string
}

func readCumulative(ctx context.Context, repo interfaces.EmbeddingCacheRepository, tenantID uint64) cumulative {
	agg, err := repo.AggregateObservations(ctx, types.EmbeddingCacheObservationFilter{TenantID: tenantID})
	if err != nil {
		return cumulative{status: "UNKNOWN"}
	}
	return cumulative{
		hit: agg.HitCount, miss: agg.MissCount, bypass: agg.BypassCount,
		lookupFail: agg.LookupFailedCount, corruption: agg.CorruptionCount,
		writeFail: agg.WriteFailedCount, provider: agg.ProviderBoundModelCallCount,
		items: agg.LogicalItemCount, status: string(agg.MeasurementStatus),
	}
}

// applySQLiteMigrations applies the formal *.up.sql migrations from dir to the
// sqlite database at dbPath using the same golang-migrate engine as the server,
// so a broken migration fails the experiment instead of being masked by
// AutoMigrate. It opens a DEDICATED *sql.DB: golang-migrate's Close() closes the
// connection it wraps, so handing it gorm's own connection would leave the
// repository below with a "sql: database is closed" handle.
func applySQLiteMigrations(dbPath, dir string) error {
	sqlDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return err
	}
	driver, err := sqlite3migrate.WithInstance(sqlDB, &sqlite3migrate.Config{})
	if err != nil {
		sqlDB.Close()
		return err
	}
	m, err := migrate.NewWithDatabaseInstance("file://"+dir, "sqlite3", driver)
	if err != nil {
		sqlDB.Close()
		return err
	}
	defer m.Close() // closes the dedicated sqlDB, not gorm's connection
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func main() {
	dbPath := flag.String("db", filepath.Join(os.TempDir(), "task005-experiment.db"), "sqlite db path")
	driver := flag.String("driver", "sqlite", "sqlite|postgres")
	dsn := flag.String("dsn", "", "postgres DSN (used when -driver=postgres)")
	outDir := flag.String("out", filepath.Join(os.TempDir(), "task005-experiment"), "report dir")
	migrationsDir := flag.String("migrations", "", "formal *.up.sql migrations directory (required for sqlite; postgres relies on the caller's formal migration)")
	flag.Parse()

	for _, d := range []string{"raw"} {
		if err := os.MkdirAll(filepath.Join(*outDir, d), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", d, err)
			os.Exit(1)
		}
	}

	cfg := embedding.Config{
		Source:                    types.ModelSourceRemote,
		BaseURL:                   "https://fixture.invalid/v1",
		ModelName:                 "fixture-embedding-model",
		Dimensions:                dimension,
		SupportsDimensionOverride: true,
		ModelID:                   "fixture-embedding-model",
		Provider:                  "fixture",
	}

	var dialector gorm.Dialector
	switch *driver {
	case "postgres":
		if *dsn == "" {
			fmt.Fprintln(os.Stderr, "-driver=postgres requires -dsn")
			os.Exit(1)
		}
		dialector = postgres.Open(*dsn)
	case "sqlite":
		dialector = sqlite.Open(*dbPath)
	default:
		fmt.Fprintf(os.Stderr, "unsupported driver %q\n", *driver)
		os.Exit(1)
	}
	// Never AutoMigrate: the experiment must prove the FORMAL migrations
	// produce a working schema, not mask a broken migration with GORM's
	// auto-DDL. PostgreSQL is migrated by the caller (run_postgres_experiment.sh
	// applies migrations/versioned via psql); SQLite applies migrations/sqlite
	// through the same golang-migrate engine the server uses. SQLite migrations
	// run BEFORE gorm.Open, on their own connection, so gorm opens a fully
	// migrated file.
	if *driver == "sqlite" {
		if *migrationsDir == "" {
			fmt.Fprintln(os.Stderr, "-driver=sqlite requires -migrations <dir>")
			os.Exit(1)
		}
		if err := applySQLiteMigrations(*dbPath, *migrationsDir); err != nil {
			fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
			os.Exit(1)
		}
	}
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", *driver, err)
		os.Exit(1)
	}
	repo := repository.NewEmbeddingCacheRepository(db)

	ctx := context.Background()
	var summary []string
	summary = append(summary, "round\tcondition\tp50_ms\tp95_ms\tmad_ms\tprovider_calls\tlogical_items\thit_count\tmiss_count\tvector_batch_digest\tmeasurement_status")
	var correctness []string
	correctness = append(correctness, "round\toff_vs_cold_vector\tcold_vs_warm_vector\toff_vs_cold_retrieval\tcold_vs_warm_retrieval")

	for r := 1; r <= rounds; r++ {
		batches := workload(r)

		off := &detProvider{}
		offEmb := embedding.WrapEmbeddingCache(off, embedding.CacheOptions{Enabled: false, TenantID: uint64(r), Config: cfg})

		cold := &detProvider{}
		coldEmb := embedding.WrapEmbeddingCache(cold, embedding.CacheOptions{Enabled: true, TenantID: uint64(r), Store: repo, Observer: repo, Config: cfg})

		warm := &detProvider{}
		warmEmb := embedding.WrapEmbeddingCache(warm, embedding.CacheOptions{Enabled: true, TenantID: uint64(r), Store: repo, Observer: repo, Config: cfg})

		offSamples := run(ctx, off, offEmb, batches, r, "OFF", repo)
		coldSamples := run(ctx, cold, coldEmb, batches, r, "ON_COLD", repo)
		warmSamples := run(ctx, warm, warmEmb, batches, r, "ON_WARM", repo)

		writeSamples(filepath.Join(*outDir, "raw"), r, "off", offSamples)
		writeSamples(filepath.Join(*outDir, "raw"), r, "cold", coldSamples)
		writeSamples(filepath.Join(*outDir, "raw"), r, "warm", warmSamples)

		offVsCold := digestsEqual(offSamples, coldSamples)
		coldVsWarm := digestsEqual(coldSamples, warmSamples)
		offVsColdRet := retrievalEqual(offSamples, coldSamples)
		coldVsWarmRet := retrievalEqual(coldSamples, warmSamples)
		correctness = append(correctness, fmt.Sprintf("%d\t%t\t%t\t%t\t%t", r, offVsCold, coldVsWarm, offVsColdRet, coldVsWarmRet))

		appendSummary(&summary, r, "OFF", offSamples, off.calls)
		appendSummary(&summary, r, "ON_COLD", coldSamples, cold.calls)
		appendSummary(&summary, r, "ON_WARM", warmSamples, warm.calls)
	}

	if err := os.WriteFile(filepath.Join(*outDir, "summary.tsv"), []byte(strings.Join(summary, "\n")+"\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write summary: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(*outDir, "correctness.tsv"), []byte(strings.Join(correctness, "\n")+"\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write correctness: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("experiment complete: %s\n", *outDir)
}

// run executes warmups then measured samples, resetting the provider counter so
// `calls` reflects measured-only provider-bound batch calls. Per-sample hit/miss
// come from the observation aggregate delta (measured samples only).
func run(ctx context.Context, prov *detProvider, emb embedding.Embedder, batches [][]string, round int, cond string, repo interfaces.EmbeddingCacheRepository) []sample {
	var out []sample
	// warmup (unmeasured)
	for i := 0; i < warmups; i++ {
		if _, err := emb.BatchEmbed(ctx, batches[i]); err != nil {
			fmt.Fprintf(os.Stderr, "round %d %s warmup: %v\n", round, cond, err)
			return out
		}
	}
	prov.calls = 0 // reset for measured-only provider count
	var prev cumulative
	if cond != "OFF" {
		prev = readCumulative(ctx, repo, uint64(round))
	}
	prevCalls := prov.calls
	for i := warmups; i < len(batches); i++ {
		start := time.Now()
		vectors, err := emb.BatchEmbed(ctx, batches[i])
		elapsed := time.Since(start).Milliseconds()
		if err != nil {
			fmt.Fprintf(os.Stderr, "round %d %s sample %d: %v\n", round, cond, i, err)
			return out
		}
		s := sample{
			Round: round, Condition: cond, SampleID: i - warmups + 1,
			ElapsedMS: elapsed, LogicalItem: itemCount, Provider: int64(prov.calls - prevCalls),
		}
		prevCalls = prov.calls
		s.VectorDigest = digestBatch(vectors)
		s.RetrievalDigest = retrievalDigest(vectors)
		if cond != "OFF" {
			cur := readCumulative(ctx, repo, uint64(round))
			s.Hit = cur.hit - prev.hit
			s.Miss = cur.miss - prev.miss
			s.Bypass = cur.bypass - prev.bypass
			s.LookupFail = cur.lookupFail - prev.lookupFail
			s.Corruption = cur.corruption - prev.corruption
			s.WriteFail = cur.writeFail - prev.writeFail
			s.Status = cur.status
			prev = cur
		}
		out = append(out, s)
	}
	return out
}

func digestsEqual(a, b []sample) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].VectorDigest != b[i].VectorDigest {
			return false
		}
	}
	return true
}

func retrievalEqual(a, b []sample) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].RetrievalDigest != b[i].RetrievalDigest {
			return false
		}
	}
	return true
}

func appendSummary(summary *[]string, r int, cond string, ss []sample, provider int) {
	p50, p95, mad := stats(ss)
	var hit, miss int64
	var st string
	for _, s := range ss {
		hit += s.Hit
		miss += s.Miss
	}
	if len(ss) > 0 {
		st = ss[len(ss)-1].Status
	}
	digest := ""
	if len(ss) > 0 {
		digest = ss[0].VectorDigest
	}
	*summary = append(*summary, fmt.Sprintf("%d\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s",
		r, cond, p50, p95, mad, provider, itemCount*samples, hit, miss, digest, st))
}

func stats(ss []sample) (p50, p95, mad int64) {
	if len(ss) == 0 {
		return 0, 0, 0
	}
	vals := make([]int64, 0, len(ss))
	for _, s := range ss {
		vals = append(vals, s.ElapsedMS)
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	quantile := func(q float64) int64 {
		idx := int(math.Ceil(q*float64(len(vals)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(vals) {
			idx = len(vals) - 1
		}
		return vals[idx]
	}
	p50 = quantile(0.5)
	p95 = quantile(0.95)
	median := p50
	var devs []int64
	for _, v := range vals {
		d := v - median
		if d < 0 {
			d = -d
		}
		devs = append(devs, d)
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i] < devs[j] })
	mad = devs[len(devs)/2]
	return
}

func writeSamples(dir string, round int, cond string, ss []sample) {
	f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s_round_%02d.json", cond, round)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "create: %v\n", err)
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	_ = enc.Encode(ss)
}
