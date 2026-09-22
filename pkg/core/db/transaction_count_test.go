package db

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestTransactionCount(t *testing.T) {
	url := os.Getenv("TEST_DB_URL")
	if url == "" {
		t.Skip("TEST_DB_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	require.NoError(t, err)
	defer conn.Close(ctx)
	schema := fmt.Sprintf("tx_count_%d", time.Now().UnixNano())
	_, err = conn.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	defer conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	_, err = conn.Exec(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	execSQL := func(sql string) { t.Helper(); _, err := conn.Exec(ctx, sql); require.NoError(t, err) }
	execSQL(`CREATE TABLE core_tx_stats (tx_hash text PRIMARY KEY, block_height bigint NOT NULL); INSERT INTO core_tx_stats VALUES ('old',1)`)
	migration, err := migrationsFS.ReadFile("sql/migrations/00039_transaction_count.sql")
	require.NoError(t, err)
	up, down, _ := strings.Cut(string(migration), "-- +migrate Down")
	execSQL("BEGIN;" + up + "COMMIT;")
	q := New(conn)
	assertCount := func(want int64) {
		t.Helper()
		got, err := q.TotalTransactions(ctx)
		require.NoError(t, err)
		require.Equal(t, want, got)
		var actual int64
		require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM core_tx_stats").Scan(&actual))
		require.Equal(t, actual, got)
	}
	assertCount(1)
	execSQL(`INSERT INTO core_tx_stats VALUES ('a',2),('b',2),('old',1) ON CONFLICT DO NOTHING`)
	assertCount(3)
	execSQL(`INSERT INTO core_tx_stats VALUES ('a',2) ON CONFLICT DO NOTHING`)
	assertCount(3)
	execSQL(`BEGIN; INSERT INTO core_tx_stats VALUES ('uncommitted',3); ROLLBACK`)
	assertCount(3)
	execSQL(`BEGIN; DELETE FROM core_tx_stats WHERE block_height=2; ROLLBACK`)
	assertCount(3)
	execSQL(`DELETE FROM core_tx_stats WHERE block_height=2`)
	assertCount(1)
	// Updating a row (including an upsert's conflict arm) does not change cardinality.
	execSQL(`INSERT INTO core_tx_stats VALUES ('old',9) ON CONFLICT(tx_hash) DO UPDATE SET block_height=excluded.block_height`)
	assertCount(1)
	execSQL(`TRUNCATE core_tx_stats`)
	assertCount(0)
	execSQL(`INSERT INTO core_tx_stats VALUES ('restored',20)`)
	assertCount(1)
	// Old snapshot: all tables truncated, data copied with triggers disabled,
	// and no counter row in the snapshot. Reconstruct exactly once afterward.
	execSQL(`TRUNCATE core_tx_count; TRUNCATE core_tx_stats; ALTER TABLE core_tx_stats DISABLE TRIGGER ALL; INSERT INTO core_tx_stats VALUES ('snapshot-a',50),('snapshot-b',50); ALTER TABLE core_tx_stats ENABLE TRIGGER ALL`)
	require.NoError(t, q.EnsureTransactionCount(ctx))
	assertCount(2)
	require.NoError(t, q.EnsureTransactionCount(ctx))
	assertCount(2)
	// New snapshot contains both tables; ensure must preserve its baseline.
	execSQL(`TRUNCATE core_tx_count; TRUNCATE core_tx_stats; ALTER TABLE core_tx_stats DISABLE TRIGGER ALL; INSERT INTO core_tx_stats VALUES ('snapshot-c',60); INSERT INTO core_tx_count VALUES (true,1); ALTER TABLE core_tx_stats ENABLE TRIGGER ALL`)
	require.NoError(t, q.EnsureTransactionCount(ctx))
	assertCount(1)
	execSQL(`INSERT INTO core_tx_stats VALUES ('after-restore',61)`)
	assertCount(2)
	execSQL(`DELETE FROM core_tx_stats WHERE block_height=61`)
	assertCount(1)
	// Restoring an older migration ledger can cause this migration to run again.
	execSQL("BEGIN;" + up + "COMMIT;")
	assertCount(1)
	// Independent writers serialize counter updates without losing increments.
	conn2, err := pgx.Connect(ctx, url)
	require.NoError(t, err)
	defer conn2.Close(ctx)
	_, err = conn2.Exec(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	done := make(chan error, 1)
	execSQL("BEGIN; INSERT INTO core_tx_stats VALUES ('writer-a',70)")
	go func() { _, err := conn2.Exec(ctx, "INSERT INTO core_tx_stats VALUES ('writer-b',70)"); done <- err }()
	execSQL("COMMIT")
	require.NoError(t, <-done)
	assertCount(3)
	// Exercise actual pg_dump/pg_restore COPY ordering with existing triggers.
	// The server disables triggers during its data phase for both old and new snapshots.
	if _, err := exec.LookPath("pg_dump"); err == nil {
		dump := filepath.Join(t.TempDir(), "counter.dump")
		run := func(name string, args ...string) {
			t.Helper()
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			require.NoError(t, err, "%s: %s", name, out)
		}
		run("pg_dump", "--dbname="+url, "--format=custom", "--data-only", "--table="+schema+".core_tx_stats", "--table="+schema+".core_tx_count", "--file="+dump)
		execSQL("TRUNCATE core_tx_count, core_tx_stats")
		run("pg_restore", "--dbname="+url, "--data-only", "--disable-triggers", "--exit-on-error", dump)
		require.NoError(t, q.EnsureTransactionCount(ctx))
		assertCount(3)
	} else {
		t.Log("pg_dump unavailable; COPY ordering tested through SQL above")
	}

	execSQL("BEGIN;" + down + "COMMIT;")
	var table *string
	require.NoError(t, conn.QueryRow(ctx, "SELECT to_regclass('core_tx_count')::text").Scan(&table))
	require.Nil(t, table)
	execSQL(`INSERT INTO core_tx_stats VALUES ('after-down',80)`)
}
