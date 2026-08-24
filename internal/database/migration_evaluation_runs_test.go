package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSQLiteMigrationsCreateEvaluationRuns(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	dbPath := filepath.Join(t.TempDir(), "migration.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))

	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	rows, err := db.Query("PRAGMA table_info(evaluation_runs)")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		require.NoError(t, rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey))
		columns[name] = true
	}
	require.NoError(t, rows.Err())

	for _, col := range []string{
		"run_id", "task_id", "tenant_id", "protocol_hash", "protocol_snapshot",
		"run_provenance", "git_commit", "app_version", "status",
		"interruption_reason", "started_at", "ended_at", "total_count",
		"processed_count", "finished_count", "metrics_json", "metrics_valid",
		"error_type", "error_message", "temporary_resource_key",
		"temporary_kb_id", "cleanup_status", "measurement_status",
		"persistence_status", "created_at", "updated_at", "deleted_at",
	} {
		require.True(t, columns[col], "evaluation_runs must have column %q", col)
	}
}
