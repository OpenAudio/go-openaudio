package server

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func setupPersistenceTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dbURL := os.Getenv("TEST_DB_URL")
	if dbURL == "" {
		t.Skip("TEST_DB_URL not set, skipping database tests")
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)

	drop := func() {
		pool.Exec(context.Background(), "DROP TABLE IF EXISTS sla_node_reports, sla_rollups, uploads CASCADE")
	}
	drop()
	// The real shapes from migration 00006: sla_node_reports references
	// sla_rollups and sorts before it.
	_, err = pool.Exec(context.Background(), `
		CREATE TABLE sla_rollups(id serial primary key);
		CREATE TABLE sla_node_reports(id serial primary key, sla_rollup_id int references sla_rollups);
		CREATE TABLE uploads(id text primary key);
		ALTER TABLE sla_node_reports SET UNLOGGED;
		ALTER TABLE sla_rollups SET UNLOGGED;
		ALTER TABLE uploads SET UNLOGGED;
	`)
	require.NoError(t, err)

	t.Cleanup(func() {
		drop()
		pool.Close()
	})
	return pool
}

func persistence(t *testing.T, pool *pgxpool.Pool, table string) string {
	t.Helper()
	var p string
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT relpersistence::text FROM pg_class WHERE relname = $1", table).Scan(&p))
	return p
}

// The old LOGGED pass ran alphabetically and left sla_node_reports UNLOGGED on
// every restore, because it references sla_rollups.
func TestSetTablesLoggedHandlesReferenceOrder(t *testing.T) {
	pool := setupPersistenceTestDB(t)

	require.NoError(t, setTablesLogged(context.Background(), pool, []string{"sla_node_reports", "sla_rollups"}))

	require.Equal(t, "p", persistence(t, pool, "sla_node_reports"))
	require.Equal(t, "p", persistence(t, pool, "sla_rollups"))
}

func TestSetTablesLoggedReportsWhatItCannotConvert(t *testing.T) {
	pool := setupPersistenceTestDB(t)

	// sla_rollups is not in the list, so sla_node_reports can never convert.
	err := setTablesLogged(context.Background(), pool, []string{"sla_node_reports"})
	require.ErrorContains(t, err, "sla_node_reports")
	require.Equal(t, "u", persistence(t, pool, "sla_node_reports"))
}

// The restore only touches snapshot tables; mediorum's share the schema.
func TestEnsureSnapshotTablesLoggedLeavesOtherTablesAlone(t *testing.T) {
	pool := setupPersistenceTestDB(t)

	require.NoError(t, ensureSnapshotTablesLogged(context.Background(), pool))

	require.Equal(t, "p", persistence(t, pool, "sla_node_reports"))
	require.Equal(t, "p", persistence(t, pool, "sla_rollups"))
	require.Equal(t, "u", persistence(t, pool, "uploads"))
}

// Startup repairs every small UNLOGGED table in public, mediorum's included:
// earlier restores made them UNLOGGED too.
func TestRepairUnloggedTablesConvertsSmallTables(t *testing.T) {
	pool := setupPersistenceTestDB(t)

	s := &Server{pool: pool, logger: zap.NewNop()}
	s.repairUnloggedTables(context.Background())

	remaining, err := unloggedPublicTables(context.Background(), pool)
	require.NoError(t, err)
	require.Empty(t, remaining)
}
