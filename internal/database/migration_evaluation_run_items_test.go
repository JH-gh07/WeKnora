package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"
)

func TestSQLiteMigrationsEvaluationRunItemsFresh(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	dbPath := filepath.Join(t.TempDir(), "items-migration.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`PRAGMA foreign_keys = ON`)
	require.NoError(t, err)

	for _, table := range []string{"evaluation_run_items", "evaluation_item_attempts"} {
		var name string
		require.NoError(t, db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name))
		require.Equal(t, table, name)
	}

	_, err = db.Exec(`INSERT INTO evaluation_runs (run_id, tenant_id) VALUES ('r1', 1)`)
	require.NoError(t, err)

	// Terminal fact protected by PRIMARY KEY (tenant_id, run_id, item_id): a
	// duplicate insert must fail, not overwrite.
	_, err = db.Exec(`INSERT INTO evaluation_run_items (tenant_id, run_id, item_id, status) VALUES (1, 'r1', 'i1', 'PENDING')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO evaluation_run_items (tenant_id, run_id, item_id, status) VALUES (1, 'r1', 'i1', 'RUNNING')`)
	require.Error(t, err, "duplicate (tenant_id,run_id,item_id) must be rejected")

	// lease_until is nullable (unclaimed item has no lease).
	var leaseUntil any
	require.NoError(t, db.QueryRow(`SELECT lease_until FROM evaluation_run_items WHERE tenant_id=1 AND run_id='r1' AND item_id='i1'`).Scan(&leaseUntil))
	require.Nil(t, leaseUntil)

	// Attempt uniqueness: (tenant_id, run_id, item_id, attempt_no).
	_, err = db.Exec(`INSERT INTO evaluation_item_attempts (tenant_id, run_id, item_id, attempt_no, fencing_token, status) VALUES (1, 'r1', 'i1', 1, 1, 'RUNNING')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO evaluation_item_attempts (tenant_id, run_id, item_id, attempt_no, fencing_token, status) VALUES (1, 'r1', 'i1', 1, 1, 'RUNNING')`)
	require.Error(t, err, "duplicate attempt_no must be rejected")

	for name, statement := range map[string]string{
		"orphan item":     `INSERT INTO evaluation_run_items (tenant_id, run_id, item_id, status) VALUES (1, 'missing', 'i2', 'PENDING')`,
		"orphan attempt":  `INSERT INTO evaluation_item_attempts (tenant_id, run_id, item_id, attempt_no, fencing_token, status) VALUES (1, 'r1', 'missing', 1, 1, 'RUNNING')`,
		"tenant zero":     `INSERT INTO evaluation_run_items (tenant_id, run_id, item_id, status) VALUES (0, 'r0', 'i0', 'PENDING')`,
		"invalid item":    `INSERT INTO evaluation_run_items (tenant_id, run_id, item_id, status) VALUES (1, 'r1', 'bad-state', 'NOT_A_STATE')`,
		"invalid attempt": `INSERT INTO evaluation_item_attempts (tenant_id, run_id, item_id, attempt_no, fencing_token, status) VALUES (1, 'r1', 'i1', 2, 2, 'NOT_A_STATE')`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := db.Exec(statement)
			require.Error(t, err)
		})
	}
}

func TestSQLiteMigrationsEvaluationRunItemsRepeatAndDown(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	dbPath := filepath.Join(t.TempDir(), "items-repeat.db")
	m := newSQLiteMigrator(t, dbPath)
	require.NoError(t, m.Up())
	latest, dirty, err := m.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(18), latest, "SQLite migration set must include the item/attempt migration")

	// Repeat up is a no-op.
	require.ErrorIs(t, m.Up(), migrate.ErrNoChange)

	// One step down drops ONLY the additive item/attempt tables.
	require.NoError(t, m.Steps(-1))
	downVersion, dirty, err := m.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, latest-1, downVersion)
	require.False(t, taskSQLiteTableExists(t, dbPath, "evaluation_run_items"), "down must drop evaluation_run_items")
	require.False(t, taskSQLiteTableExists(t, dbPath, "evaluation_item_attempts"), "down must drop evaluation_item_attempts")
	require.True(t, taskSQLiteTableExists(t, dbPath, "evaluation_runs"), "down must preserve evaluation_runs")

	// Up again restores the tables.
	require.NoError(t, m.Up())
	upVersion, dirty, err := m.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, latest, upVersion)
	require.True(t, taskSQLiteTableExists(t, dbPath, "evaluation_run_items"), "up must restore evaluation_run_items")
	require.True(t, taskSQLiteTableExists(t, dbPath, "evaluation_item_attempts"), "up must restore evaluation_item_attempts")
}

// TestMigrationSchemaParitySQLiteVsPostgres proves the SQLite and PostgreSQL
// migrations define the SAME column names in the SAME order for the item and
// attempt fact tables (dual-DB schema parity, AC12). Column types legitimately
// differ (DATETIME vs TIMESTAMPTZ, TEXT vs JSONB); column identity must not.
func TestMigrationSchemaParitySQLiteVsPostgres(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	sqliteCols := migrationTableColumns(t, filepath.Join(repoRoot, "migrations", "sqlite", "000018_evaluation_run_items.up.sql"))
	pgCols := migrationTableColumns(t, filepath.Join(repoRoot, "migrations", "versioned", "000095_evaluation_run_items.up.sql"))

	require.ElementsMatch(t, []string{"evaluation_run_items", "evaluation_item_attempts"}, sortedKeys(sqliteCols), "unexpected SQLite tables")
	for _, table := range []string{"evaluation_run_items", "evaluation_item_attempts"} {
		require.Equal(t, pgCols[table], sqliteCols[table], "column name/order mismatch for table %s", table)
	}
}

// migrationTableColumns extracts, per table, the ordered column names from a
// CREATE TABLE migration file. It ignores type/default differences and PRIMARY
// KEY lines so only column identity is compared.
func migrationTableColumns(t *testing.T, path string) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	re := regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS (\w+) \((.*?)\);`)
	out := map[string][]string{}
	for _, m := range re.FindAllStringSubmatch(string(data), -1) {
		table := m[1]
		var cols []string
		for _, line := range strings.Split(m[2], "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "PRIMARY KEY") || strings.HasPrefix(line, "CREATE INDEX") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) > 0 {
				cols = append(cols, fields[0])
			}
		}
		out[table] = cols
	}
	return out
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Two fixed tables; simple insertion order is deterministic here but sort for
	// clarity.
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}
