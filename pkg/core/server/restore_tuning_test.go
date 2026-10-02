package server

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestPgRestoreEnvAddsSessionOptions(t *testing.T) {
	env := pgRestoreEnv([]string{"PATH=/bin", "PGPASSWORD=x"})
	require.Equal(t, []string{"PATH=/bin", "PGPASSWORD=x", "PGOPTIONS=-c synchronous_commit=off"}, env)
}

func TestPgRestoreEnvKeepsOperatorOptions(t *testing.T) {
	env := pgRestoreEnv([]string{"PGOPTIONS=-c statement_timeout=0", "PATH=/bin"})
	require.Equal(t, []string{"PATH=/bin", "PGOPTIONS=-c statement_timeout=0 -c synchronous_commit=off"}, env)
}

func TestLeftoverRestoreSettings(t *testing.T) {
	dbURL := os.Getenv("TEST_DB_URL")
	if dbURL == "" {
		t.Skip("TEST_DB_URL not set, skipping database tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	// Registered first so it runs last, after the settings are reset.
	t.Cleanup(pool.Close)

	reset := func() {
		for _, sql := range []string{
			"ALTER SYSTEM RESET synchronous_commit",
			"ALTER SYSTEM RESET max_wal_size",
			"ALTER SYSTEM RESET checkpoint_timeout",
			"SELECT pg_reload_conf()",
		} {
			_, err := pool.Exec(ctx, sql)
			require.NoError(t, err)
		}
	}
	reset()
	t.Cleanup(reset)

	settings, err := leftoverRestoreSettings(ctx, pool)
	require.NoError(t, err)
	require.Empty(t, settings)

	// What earlier restores ran.
	for _, sql := range []string{
		"ALTER SYSTEM SET synchronous_commit = off",
		"ALTER SYSTEM SET max_wal_size = '8GB'",
		"ALTER SYSTEM SET checkpoint_timeout = '1h'",
		"SELECT pg_reload_conf()",
	} {
		_, err := pool.Exec(ctx, sql)
		require.NoError(t, err)
	}

	// pg_reload_conf only signals the server; sessions pick it up shortly after.
	require.Eventually(t, func() bool {
		settings, err = leftoverRestoreSettings(ctx, pool)
		return err == nil && len(settings) == 3
	}, 5*time.Second, 100*time.Millisecond)
	require.Equal(t, []persistedSetting{
		{Name: "checkpoint_timeout", Value: "3600"},
		{Name: "max_wal_size", Value: "8192"},
		{Name: "synchronous_commit", Value: "off"},
	}, settings)
}

func setupSettingsTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("TEST_DB_URL")
	if dbURL == "" {
		t.Skip("TEST_DB_URL not set, skipping database tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	// Registered first so it runs last, after the settings are reset.
	t.Cleanup(pool.Close)

	reset := func() {
		for _, sql := range []string{
			"ALTER SYSTEM RESET max_wal_size",
			"ALTER SYSTEM RESET checkpoint_timeout",
			"SELECT pg_reload_conf()",
		} {
			_, err := pool.Exec(ctx, sql)
			require.NoError(t, err)
		}
	}
	reset()
	t.Cleanup(reset)
	return pool
}

// eventuallySetting waits for a reload to reach the pool's sessions and checks
// a setting's value and whether it comes from postgresql.auto.conf.
func eventuallySetting(t *testing.T, pool *pgxpool.Pool, name, want string, wantAutoConf bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		var setting string
		var autoConf bool
		err := pool.QueryRow(context.Background(), `
			SELECT setting, coalesce(sourcefile LIKE '%postgresql.auto.conf', false)
			FROM pg_settings WHERE name = $1`, name).Scan(&setting, &autoConf)
		return err == nil && setting == want && autoConf == wantAutoConf
	}, 5*time.Second, 100*time.Millisecond, "%s should be %s (auto.conf=%v)", name, want, wantAutoConf)
}

func TestRestoreServerSettingsAreUndone(t *testing.T) {
	pool := setupSettingsTestDB(t)
	ctx := context.Background()

	var defaultWal string
	require.NoError(t, pool.QueryRow(ctx, "SELECT setting FROM pg_settings WHERE name = 'max_wal_size'").Scan(&defaultWal))

	undo, err := applyRestoreServerSettings(ctx, pool)
	require.NoError(t, err)
	eventuallySetting(t, pool, "max_wal_size", "8192", true)
	eventuallySetting(t, pool, "checkpoint_timeout", "3600", true)

	require.NoError(t, undo(ctx))
	eventuallySetting(t, pool, "max_wal_size", defaultWal, false)

	settings, err := leftoverRestoreSettings(ctx, pool)
	require.NoError(t, err)
	require.Empty(t, settings)
}

// An operator's own ALTER SYSTEM value is put back.
func TestRestoreServerSettingsKeepOperatorValues(t *testing.T) {
	pool := setupSettingsTestDB(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, "ALTER SYSTEM SET max_wal_size = '3GB'")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "SELECT pg_reload_conf()")
	require.NoError(t, err)
	eventuallySetting(t, pool, "max_wal_size", "3072", true)

	undo, err := applyRestoreServerSettings(ctx, pool)
	require.NoError(t, err)
	eventuallySetting(t, pool, "max_wal_size", "8192", true)

	require.NoError(t, undo(ctx))
	eventuallySetting(t, pool, "max_wal_size", "3072", true)
}

// A value an earlier restore left behind is removed, not put back.
func TestRestoreServerSettingsClearEarlierLeftovers(t *testing.T) {
	pool := setupSettingsTestDB(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, "ALTER SYSTEM SET max_wal_size = '8GB'")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "SELECT pg_reload_conf()")
	require.NoError(t, err)
	eventuallySetting(t, pool, "max_wal_size", "8192", true)

	undo, err := applyRestoreServerSettings(ctx, pool)
	require.NoError(t, err)
	require.NoError(t, undo(ctx))

	require.Eventually(t, func() bool {
		var setting string
		var autoConf bool
		err := pool.QueryRow(ctx, `SELECT setting, coalesce(sourcefile LIKE '%postgresql.auto.conf', false)
			FROM pg_settings WHERE name = 'max_wal_size'`).Scan(&setting, &autoConf)
		return err == nil && !autoConf && setting != "8192"
	}, 5*time.Second, 100*time.Millisecond)
}
