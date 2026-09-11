package database

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTask016PrerequisiteReconciliationIsIdempotent protects upgrades from
// long-lived development databases whose migration watermark advanced past an
// additive prerequisite before the final migration numbering was frozen.
func TestTask016PrerequisiteReconciliationIsIdempotent(t *testing.T) {
	t.Parallel()

	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(filename), "..", "..", "migrations", "versioned",
		"000096_reconcile_task016_prerequisites.up.sql")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	sql := strings.ToUpper(string(raw))

	for _, table := range []string{
		"TENANT_SKILLS", "TENANT_SKILL_SNAPSHOTS", "TENANT_USER_ENV_VARS",
	} {
		require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS "+table)
	}
	for _, column := range []string{
		"MESSAGES ADD COLUMN IF NOT EXISTS USAGE",
		"TENANT_SKILLS ADD COLUMN IF NOT EXISTS INSTALL_SESSION_ID",
		"TENANT_SKILLS ADD COLUMN IF NOT EXISTS INSTALL_MESSAGE_ID",
		"TENANT_SKILLS ADD COLUMN IF NOT EXISTS ENVS",
		"TENANT_SKILL_SNAPSHOTS ADD COLUMN IF NOT EXISTS PLANNED_NAME",
	} {
		require.Contains(t, sql, column)
	}
	for _, index := range []string{
		"UQ_TENANT_SKILLS_CONFIG_NAME", "IDX_TENANT_SKILL_SNAPSHOTS_CONFIG",
		"IDX_TENANT_SKILL_SNAPSHOTS_STATE", "UQ_USER_ENV_VAR",
		"IDX_USER_ENV_VAR_SKILL", "IDX_USER_ENV_VAR_CONFIG",
	} {
		require.Contains(t, sql, "INDEX IF NOT EXISTS "+index)
	}
}
