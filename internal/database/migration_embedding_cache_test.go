package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"
)

func TestSQLiteMigrationsCreateEmbeddingCacheTables(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	dbPath := filepath.Join(t.TempDir(), "embedding-cache-migration.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	for _, table := range []string{"embedding_cache_entries", "embedding_cache_observations"} {
		var name string
		require.NoError(t, db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name))
		require.Equal(t, table, name)
	}
}

func TestSQLiteMigrationsEmbeddingCacheUniqueConstraint(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	dbPath := filepath.Join(t.TempDir(), "embedding-cache-unique.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// Same tenant + same key must conflict; different tenant or key must not.
	_, err = db.Exec(`INSERT INTO embedding_cache_entries (id, tenant_id, cache_key, vector_payload) VALUES ('e1', 1, 'k1', '[1,2,3]')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO embedding_cache_entries (id, tenant_id, cache_key, vector_payload) VALUES ('e2', 1, 'k1', '[4,5,6]')`)
	require.Error(t, err, "same tenant + cache_key must violate the unique constraint")
	_, err = db.Exec(`INSERT INTO embedding_cache_entries (id, tenant_id, cache_key, vector_payload) VALUES ('e3', 1, 'k2', '[1,2,3]')`)
	require.NoError(t, err, "different key same tenant is allowed")
	_, err = db.Exec(`INSERT INTO embedding_cache_entries (id, tenant_id, cache_key, vector_payload) VALUES ('e4', 2, 'k1', '[1,2,3]')`)
	require.NoError(t, err, "different tenant same key is allowed")
}

func TestSQLiteMigrationsEmbeddingCacheRepeatAndDown(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	dbPath := filepath.Join(t.TempDir(), "embedding-cache-down.db")
	m := newSQLiteMigrator(t, dbPath)

	require.NoError(t, m.Up())
	latest, dirty, err := m.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(8), latest, "SQLite migration set must include the embedding cache migration")

	require.ErrorIs(t, m.Up(), migrate.ErrNoChange)

	// One step down removes the cache tables.
	require.NoError(t, m.Steps(-1))
	require.True(t, sqliteTableExists(t, dbPath, "model_calls"), "down must preserve model_calls")
	require.False(t, sqliteTableExists(t, dbPath, "embedding_cache_entries"), "down must drop entries")
	require.False(t, sqliteTableExists(t, dbPath, "embedding_cache_observations"), "down must drop observations")

	require.NoError(t, m.Up())
	require.True(t, sqliteTableExists(t, dbPath, "embedding_cache_entries"), "up must recreate entries")
	require.True(t, sqliteTableExists(t, dbPath, "embedding_cache_observations"), "up must recreate observations")
}
