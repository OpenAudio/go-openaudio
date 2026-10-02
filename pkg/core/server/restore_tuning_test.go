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
