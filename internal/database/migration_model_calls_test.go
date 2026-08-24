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

// TestSQLiteMigrationsModelCallsRepeatAndDown proves the model_calls migration is
// idempotent (repeat up is a no-op) and reversible (down drops both tables, up
// recreates them), rather than only exercising a fresh up.
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
	require.Equal(t, uint(6), latest, "SQLite migration set must still top out at version 6")

	// Repeat up is a no-op, not an error.
	require.ErrorIs(t, m.Up(), migrate.ErrNoChange)

	// Down drops model_calls + model_metering_health. Use Steps(-1): Down()
	// migrates all the way to the nil version, not a single step.
	require.NoError(t, m.Steps(-1))
	downVersion, dirty, err := m.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, latest-1, downVersion)
	require.False(t, sqliteTableExists(t, dbPath, "model_calls"), "down must drop model_calls")
	require.False(t, sqliteTableExists(t, dbPath, "model_metering_health"), "down must drop model_metering_health")

	// Up again recreates both tables.
	require.NoError(t, m.Up())
	upVersion, dirty, err := m.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, latest, upVersion)
	require.True(t, sqliteTableExists(t, dbPath, "model_calls"), "up must recreate model_calls")
	require.True(t, sqliteTableExists(t, dbPath, "model_metering_health"), "up must recreate model_metering_health")
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

func sqliteTableExists(t *testing.T, dbPath, table string) bool {
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
