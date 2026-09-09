package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	sqlite3migrate "github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/stretchr/testify/require"
)

func TestSQLiteMigrationsCreateModelCallsWithNullableUnknowns(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })
	dbPath := filepath.Join(t.TempDir(), "model-call-migration.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`INSERT INTO model_calls (id, tenant_id, model_id, operation, usage_finality, cache_status, success, attempt_observability) VALUES ('null-call', 1, 'm', 'chat', 'unavailable', 'unreported', 1, 'unobservable')`)
	require.NoError(t, err)
	var runID, inputTokens, estimatedCost any
	require.NoError(t, db.QueryRow(`SELECT run_id, input_tokens, estimated_cost FROM model_calls WHERE id='null-call'`).Scan(&runID, &inputTokens, &estimatedCost))
	require.Nil(t, runID)
	require.Nil(t, inputTokens)
	require.Nil(t, estimatedCost)
	for _, table := range []string{"model_calls", "model_metering_health"} {
		var name string
		require.NoError(t, db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name))
		require.Equal(t, table, name)
	}
}

// TestSQLiteMigrationsModelCallsRepeatAndDown proves the latest additive
// model-call migration is idempotent and reversible without dropping the
// existing model_calls facts.
func TestSQLiteMigrationsModelCallsRepeatAndDown(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	dbPath := filepath.Join(t.TempDir(), "model-call-migration.db")
	m := newSQLiteMigrator(t, dbPath)

	require.NoError(t, m.Up())
	latest, dirty, err := m.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(18), latest, "SQLite migration set must include the item/attempt migration")

	// Repeat up is a no-op, not an error.
	require.ErrorIs(t, m.Up(), migrate.ErrNoChange)

	// Two steps down drop the item/attempt migration (000018) and the pricing
	// snapshot migration (000017). The model_calls facts and earlier additive
	// columns survive.
	require.NoError(t, m.Steps(-2))
	downVersion, dirty, err := m.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, latest-2, downVersion)
	require.True(t, taskSQLiteTableExists(t, dbPath, "model_calls"), "down must preserve model_calls")
	require.False(t, taskSQLiteColumnExists(t, dbPath, "model_calls", "pricing_status"), "down must remove the pricing snapshot columns")
	require.False(t, taskSQLiteColumnExists(t, dbPath, "model_calls", "estimated_cost_nanos"), "down must remove estimated_cost_nanos")
	require.True(t, taskSQLiteColumnExists(t, dbPath, "model_calls", "cache_reported_input_tokens"), "down must not remove earlier additive column")

	// Up again restores the pricing snapshot columns.
	require.NoError(t, m.Up())
	upVersion, dirty, err := m.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, latest, upVersion)
	require.True(t, taskSQLiteColumnExists(t, dbPath, "model_calls", "pricing_status"), "up must restore pricing snapshot columns")
	require.True(t, taskSQLiteColumnExists(t, dbPath, "model_calls", "estimated_cost_nanos"), "up must restore estimated_cost_nanos")
}

// TestSQLiteMigrationsPricingSnapshotNullableAndRoundTrip proves the additive
// pricing snapshot columns are nullable (legacy rows stay NULL) and carry a
// priced row's fixed-point facts intact.
func TestSQLiteMigrationsPricingSnapshotNullableAndRoundTrip(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	dbPath := filepath.Join(t.TempDir(), "pricing-snapshot-migration.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// Legacy row: no pricing snapshot columns set.
	_, err = db.Exec(`INSERT INTO model_calls (id, tenant_id, model_id, operation, usage_finality, cache_status, success, attempt_observability) VALUES ('legacy', 1, 'm', 'chat', 'unavailable', 'unreported', 1, 'unobservable')`)
	require.NoError(t, err)
	var pricingStatus, pricingReason, pricingUnit, estimatedCostNanos any
	require.NoError(t, db.QueryRow(`SELECT pricing_status, pricing_reason, pricing_unit, estimated_cost_nanos FROM model_calls WHERE id='legacy'`).Scan(&pricingStatus, &pricingReason, &pricingUnit, &estimatedCostNanos))
	require.Nil(t, pricingStatus)
	require.Nil(t, pricingReason)
	require.Nil(t, pricingUnit)
	require.Nil(t, estimatedCostNanos)

	// Priced row round trip.
	_, err = db.Exec(`INSERT INTO model_calls (id, tenant_id, model_id, operation, usage_finality, cache_status, success, attempt_observability, pricing_status, pricing_reason, pricing_rule_id, pricing_catalog_hash, pricing_unit, input_unit_price_nanos_per_million, output_unit_price_nanos_per_million, estimated_cost_nanos) VALUES ('priced', 1, 'm', 'chat', 'reported', 'unreported', 1, 'unobservable', 'PRICED', '', 'r1', 'h1', 'per_1m_tokens', 500000000, 2000000000, 150000)`)
	require.NoError(t, err)
	var gotStatus string
	var gotInput, gotOutput, gotNanos int64
	require.NoError(t, db.QueryRow(`SELECT pricing_status, input_unit_price_nanos_per_million, output_unit_price_nanos_per_million, estimated_cost_nanos FROM model_calls WHERE id='priced'`).Scan(&gotStatus, &gotInput, &gotOutput, &gotNanos))
	require.Equal(t, "PRICED", gotStatus)
	require.Equal(t, int64(500000000), gotInput)
	require.Equal(t, int64(2000000000), gotOutput)
	require.Equal(t, int64(150000), gotNanos)
}

func taskSQLiteColumnExists(t *testing.T, dbPath, table, column string) bool {
	t.Helper()
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		require.NoError(t, rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey))
		if name == column {
			return true
		}
	}
	require.NoError(t, rows.Err())
	return false
}

func newSQLiteMigrator(t *testing.T, dbPath string) *migrate.Migrate {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	driver, err := sqlite3migrate.WithInstance(sqlDB, &sqlite3migrate.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithDatabaseInstance("file://migrations/sqlite", "sqlite3", driver)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })
	return m
}

func taskSQLiteTableExists(t *testing.T, dbPath, table string) bool {
	t.Helper()
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	var name string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
	if err == sql.ErrNoRows {
		return false
	}
	require.NoError(t, err)
	return name == table
}
