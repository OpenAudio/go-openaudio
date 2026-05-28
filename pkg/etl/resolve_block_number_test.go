package etl

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// setupBlocksTestDB returns a pool with ETL migrations applied and the
// `blocks` table truncated. Skips if ETL_TEST_DB_URL is unset.
func setupBlocksTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("ETL_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("ETL_TEST_DB_URL not set")
	}
	logger, _ := zap.NewDevelopment()
	if err := db.RunMigrations(logger, dbURL, true); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if _, err := pool.Exec(context.Background(), `TRUNCATE blocks CASCADE`); err != nil {
		t.Fatalf("truncate blocks: %v", err)
	}
	return pool
}

func newTestIndexer(pool *pgxpool.Pool) *Indexer {
	return &Indexer{pool: pool, logger: zap.NewNop()}
}

// callResolveBlockNumber wraps resolveBlockNumber in its own transaction
// for the test. Production callers (processBlock) own the tx; in tests
// we provide one inline so the assertions can read the committed state.
func callResolveBlockNumber(t *testing.T, pool *pgxpool.Pool, ix *Indexer, blockHash string) (int64, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	num, err := ix.resolveBlockNumber(ctx, tx, blockHash)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit tx: %v", err)
	}
	return num, nil
}

// countCurrentTips counts how many blocks have is_current = true. The
// partial unique index guarantees this is at most 1 in steady state;
// we use it to assert the tip invariant after every resolveBlockNumber call.
func countCurrentTips(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM blocks WHERE is_current IS TRUE`).Scan(&n); err != nil {
		t.Fatalf("count tips: %v", err)
	}
	return n
}

// currentTipHash returns the blockhash of the unique is_current=true row.
// Fails the test if zero or more than one row matches.
func currentTipHash(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var h string
	if err := pool.QueryRow(context.Background(),
		`SELECT blockhash FROM blocks WHERE is_current IS TRUE`).Scan(&h); err != nil {
		t.Fatalf("read current tip hash: %v", err)
	}
	return h
}

// blockNumber returns the number stored for a given blockhash, failing the
// test if no row exists.
func blockNumber(t *testing.T, pool *pgxpool.Pool, hash string) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(),
		`SELECT number FROM blocks WHERE blockhash = $1`, hash).Scan(&n); err != nil {
		t.Fatalf("read block number for %s: %v", hash, err)
	}
	return n
}

// blockRowCount returns the total number of rows in `blocks`. Used to
// catch unintended duplicate inserts.
func blockRowCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM blocks`).Scan(&n); err != nil {
		t.Fatalf("count blocks: %v", err)
	}
	return n
}

// TestResolveBlockNumber_EmptyTable: on a fresh table, the first call
// inserts the block at number=1 and marks it the unique tip.
func TestResolveBlockNumber_EmptyTable(t *testing.T) {
	pool := setupBlocksTestDB(t)
	ix := newTestIndexer(pool)

	got, err := callResolveBlockNumber(t, pool, ix, "blk-genesis")
	if err != nil {
		t.Fatalf("resolveBlockNumber: %v", err)
	}
	if got != 1 {
		t.Errorf("returned number = %d, want 1", got)
	}
	if tips := countCurrentTips(t, pool); tips != 1 {
		t.Errorf("is_current=true count = %d, want 1 (tip invariant)", tips)
	}
	if tip := currentTipHash(t, pool); tip != "blk-genesis" {
		t.Errorf("tip hash = %s, want blk-genesis", tip)
	}
}

// TestResolveBlockNumber_AppendsToChain: with an existing chain, the
// next call increments the number, demotes the prior tip, and promotes
// the new block. Tip invariant holds throughout.
func TestResolveBlockNumber_AppendsToChain(t *testing.T) {
	pool := setupBlocksTestDB(t)
	ix := newTestIndexer(pool)

	// Build up a 3-block chain via the function under test.
	for i, hash := range []string{"blk-1", "blk-2", "blk-3"} {
		n, err := callResolveBlockNumber(t, pool, ix, hash)
		if err != nil {
			t.Fatalf("step %d resolveBlockNumber: %v", i, err)
		}
		if want := int64(i + 1); n != want {
			t.Errorf("step %d returned %d, want %d", i, n, want)
		}
		if tips := countCurrentTips(t, pool); tips != 1 {
			t.Errorf("step %d tip count = %d, want 1", i, tips)
		}
		if tip := currentTipHash(t, pool); tip != hash {
			t.Errorf("step %d tip = %s, want %s", i, tip, hash)
		}
	}
}

// TestResolveBlockNumber_AdoptsExistingByHash: when a row already
// exists for this hash (e.g. another writer wrote it during a cutover),
// the function adopts its number without inserting a new row and
// promotes it to the tip.
func TestResolveBlockNumber_AdoptsExistingByHash(t *testing.T) {
	pool := setupBlocksTestDB(t)
	ctx := context.Background()

	// Simulate a co-existing writer's row at number=42, is_current=false.
	// Plus a separate "stale tip" at number=43 with is_current=true,
	// which our call should demote.
	if _, err := pool.Exec(ctx, `
		INSERT INTO blocks (blockhash, parenthash, number, is_current) VALUES
		  ('blk-prewritten', NULL, 42, false),
		  ('blk-stale-tip', NULL, 43, true)
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ix := newTestIndexer(pool)
	got, err := callResolveBlockNumber(t, pool, ix, "blk-prewritten")
	if err != nil {
		t.Fatalf("resolveBlockNumber: %v", err)
	}
	if got != 42 {
		t.Errorf("returned number = %d, want 42 (adopted)", got)
	}
	if rows := blockRowCount(t, pool); rows != 2 {
		t.Errorf("row count = %d, want 2 (no new row inserted)", rows)
	}
	if tips := countCurrentTips(t, pool); tips != 1 {
		t.Errorf("tip count = %d, want 1", tips)
	}
	if tip := currentTipHash(t, pool); tip != "blk-prewritten" {
		t.Errorf("tip = %s, want blk-prewritten (promoted from is_current=false)", tip)
	}
}

// TestResolveBlockNumber_TipInvariantAfterFossils: a co-running writer
// left "fossil" rows ahead of where we last knew MAX(number) to be —
// possibly with one of them still flagged is_current=true — then died.
// We must skip past all fossils, insert at the true MAX+1, and end up
// as the unique tip. This is the production failure mode that dropped
// ray52726.
func TestResolveBlockNumber_TipInvariantAfterFossils(t *testing.T) {
	pool := setupBlocksTestDB(t)
	ctx := context.Background()

	// Fossils 100..109, with the last one still flagged as the tip — as
	// if the prior writer was killed mid-stream after marking 109 current.
	for i := 100; i <= 108; i++ {
		if _, err := pool.Exec(ctx,
			`INSERT INTO blocks (blockhash, parenthash, number, is_current)
			 VALUES ($1, NULL, $2, false)`,
			"fossil-"+strconv.Itoa(i), i); err != nil {
			t.Fatalf("seed fossil %d: %v", i, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO blocks (blockhash, parenthash, number, is_current)
		 VALUES ('fossil-109-tip', NULL, 109, true)`); err != nil {
		t.Fatalf("seed tip fossil: %v", err)
	}

	ix := newTestIndexer(pool)
	got, err := callResolveBlockNumber(t, pool, ix, "blk-new")
	if err != nil {
		t.Fatalf("resolveBlockNumber: %v", err)
	}
	if got != 110 {
		t.Errorf("returned %d, want 110 (MAX(109)+1)", got)
	}
	if tips := countCurrentTips(t, pool); tips != 1 {
		t.Errorf("tip count = %d, want 1", tips)
	}
	if tip := currentTipHash(t, pool); tip != "blk-new" {
		t.Errorf("tip = %s, want blk-new", tip)
	}
}

// TestResolveBlockNumber_IdempotentSameHash: calling twice for the
// same hash returns the same number both times, doesn't double-insert,
// and the tip invariant holds.
func TestResolveBlockNumber_IdempotentSameHash(t *testing.T) {
	pool := setupBlocksTestDB(t)
	ix := newTestIndexer(pool)

	first, err := callResolveBlockNumber(t, pool, ix, "blk-X")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := callResolveBlockNumber(t, pool, ix, "blk-X")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first != second {
		t.Errorf("first=%d second=%d, want equal", first, second)
	}
	if rows := blockRowCount(t, pool); rows != 1 {
		t.Errorf("row count = %d, want 1", rows)
	}
	if tips := countCurrentTips(t, pool); tips != 1 {
		t.Errorf("tip count = %d, want 1", tips)
	}
}

// TestResolveBlockNumber_ParentHashIsPriorTip: the parenthash field on
// the newly-inserted block is set to the previous tip's blockhash.
// (Cheap correctness check that we capture parenthash before demoting.)
func TestResolveBlockNumber_ParentHashIsPriorTip(t *testing.T) {
	pool := setupBlocksTestDB(t)
	ctx := context.Background()
	ix := newTestIndexer(pool)

	if _, err := callResolveBlockNumber(t, pool, ix, "blk-A"); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if _, err := callResolveBlockNumber(t, pool, ix, "blk-B"); err != nil {
		t.Fatalf("insert B: %v", err)
	}

	var parent *string
	if err := pool.QueryRow(ctx,
		`SELECT parenthash FROM blocks WHERE blockhash = 'blk-B'`).Scan(&parent); err != nil {
		t.Fatalf("read parenthash: %v", err)
	}
	if parent == nil || *parent != "blk-A" {
		got := "<nil>"
		if parent != nil {
			got = *parent
		}
		t.Errorf("blk-B.parenthash = %s, want blk-A", got)
	}
}

// TestResolveBlockNumber_NumberValueIsStoredCorrectly: the number we
// return is the same one stored in the DB row. (Guards against an
// off-by-one between INSERT and SELECT.)
func TestResolveBlockNumber_NumberValueIsStoredCorrectly(t *testing.T) {
	pool := setupBlocksTestDB(t)
	ix := newTestIndexer(pool)

	got, err := callResolveBlockNumber(t, pool, ix, "blk-Y")
	if err != nil {
		t.Fatalf("resolveBlockNumber: %v", err)
	}
	if stored := blockNumber(t, pool, "blk-Y"); stored != got {
		t.Errorf("returned %d, stored %d", got, stored)
	}
}

