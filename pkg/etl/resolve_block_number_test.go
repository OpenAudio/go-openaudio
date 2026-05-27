package etl

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// setupResolveDB returns a fresh test pool with ETL migrations applied
// and the blocks table truncated. Skips if ETL_TEST_DB_URL is unset.
func setupResolveDB(t *testing.T) *pgxpool.Pool {
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

	// Wipe blocks for a deterministic starting state. We use TRUNCATE so
	// auto-cascade clears any FK'd test rows from prior runs.
	if _, err := pool.Exec(context.Background(), `TRUNCATE blocks CASCADE`); err != nil {
		t.Fatalf("truncate blocks: %v", err)
	}
	return pool
}

func newTestIndexer(pool *pgxpool.Pool, lastEmBlock int64) *Indexer {
	return &Indexer{
		pool:        pool,
		lastEmBlock: lastEmBlock,
		logger:      zap.NewNop(),
	}
}

// TestResolveBlockNumber_AdoptsExistingByHash exercises the cutover happy
// path: another writer (legacy Python indexer) already wrote this block,
// so resolveBlockNumber should return that row's number without inserting
// anything new. This is the fix for the bug that dropped ray52726 — the
// previous insertCurrentBlock would have errored on `blocks_number_key`
// and the caller would have skipped the entire CometBFT block.
func TestResolveBlockNumber_AdoptsExistingByHash(t *testing.T) {
	pool := setupResolveDB(t)
	ctx := context.Background()

	const existingHash = "blk-existing-hash"
	const existingNumber int64 = 12345

	if _, err := pool.Exec(ctx, `
		INSERT INTO blocks (blockhash, parenthash, number, is_current)
		VALUES ($1, NULL, $2, true)
	`, existingHash, existingNumber); err != nil {
		t.Fatalf("seed existing block: %v", err)
	}

	// lastEmBlock is intentionally far behind — we want to confirm we
	// adopt the existing number rather than inventing our own.
	ix := newTestIndexer(pool, 500)
	got, err := ix.resolveBlockNumber(existingHash, 99999)
	if err != nil {
		t.Fatalf("resolveBlockNumber: %v", err)
	}
	if got != existingNumber {
		t.Errorf("got number %d, want %d", got, existingNumber)
	}

	// We must not have inserted a new row.
	var cnt int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM blocks WHERE blockhash = $1`, existingHash).Scan(&cnt); err != nil {
		t.Fatalf("count blocks: %v", err)
	}
	if cnt != 1 {
		t.Errorf("blocks for hash %s = %d, want 1 (no extra insert)", existingHash, cnt)
	}

	// lastEmBlock must not have been touched: we adopted an existing row,
	// we didn't claim a new number.
	if ix.lastEmBlock != 500 {
		t.Errorf("lastEmBlock = %d, want 500 (unchanged when adopting)", ix.lastEmBlock)
	}
}

// TestResolveBlockNumber_ResyncsFromFossils exercises the post-cutover
// recovery path: a writer wrote ahead of our in-memory counter and then
// died, leaving "fossil" rows. We must re-read MAX(number) and pick up
// from there, not blindly increment from our stale lastEmBlock.
//
// This is the bug we hit in prod: ETL started with lastEmBlock=192 from
// MAX(number) at boot. Python then wrote 193..260 over the next ~12 min
// before being killed. ETL never re-read MAX, so it kept trying to
// INSERT at 193, 194, 195 — all conflicting with Python's fossils.
func TestResolveBlockNumber_ResyncsFromFossils(t *testing.T) {
	pool := setupResolveDB(t)
	ctx := context.Background()

	// Seed 11 fossil rows from a hypothetical dead writer.
	for i := int64(1000); i <= 1010; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO blocks (blockhash, parenthash, number, is_current)
			VALUES ($1, NULL, $2, false)
		`, fmt.Sprintf("fossil-%d", i), i); err != nil {
			t.Fatalf("seed fossil %d: %v", i, err)
		}
	}

	// Our in-memory counter is stale at 500.
	ix := newTestIndexer(pool, 500)

	got, err := ix.resolveBlockNumber("fresh-block-hash", 555)
	if err != nil {
		t.Fatalf("resolveBlockNumber: %v", err)
	}
	// Should have resynced lastEmBlock to 1010 (the actual MAX) and
	// then incremented to 1011 for the new block.
	const want int64 = 1011
	if got != want {
		t.Errorf("got number %d, want %d (resync to MAX=1010 + 1)", got, want)
	}
	if ix.lastEmBlock != want {
		t.Errorf("lastEmBlock = %d, want %d", ix.lastEmBlock, want)
	}

	// Confirm the row landed in the DB.
	var num int64
	if err := pool.QueryRow(ctx, `SELECT number FROM blocks WHERE blockhash = 'fresh-block-hash'`).Scan(&num); err != nil {
		t.Fatalf("verify insert: %v", err)
	}
	if num != want {
		t.Errorf("DB row number = %d, want %d", num, want)
	}
}

// TestResolveBlockNumber_FreshInsert exercises the steady-state path:
// no other writers, no fossils, lastEmBlock matches MAX. Should just
// increment and insert.
func TestResolveBlockNumber_FreshInsert(t *testing.T) {
	pool := setupResolveDB(t)
	ctx := context.Background()

	// Seed one current block at number=50 (our boot baseline).
	if _, err := pool.Exec(ctx, `
		INSERT INTO blocks (blockhash, parenthash, number, is_current)
		VALUES ('baseline-block', NULL, 50, true)
	`); err != nil {
		t.Fatalf("seed baseline: %v", err)
	}

	ix := newTestIndexer(pool, 50) // matches MAX, no resync needed

	got, err := ix.resolveBlockNumber("new-block", 100)
	if err != nil {
		t.Fatalf("resolveBlockNumber: %v", err)
	}
	if got != 51 {
		t.Errorf("got number %d, want 51", got)
	}
	if ix.lastEmBlock != 51 {
		t.Errorf("lastEmBlock = %d, want 51", ix.lastEmBlock)
	}

	// Verify is_current was correctly swapped to the new row.
	var currentHash string
	if err := pool.QueryRow(ctx, `SELECT blockhash FROM blocks WHERE is_current IS TRUE`).Scan(&currentHash); err != nil {
		t.Fatalf("query current: %v", err)
	}
	if currentHash != "new-block" {
		t.Errorf("is_current=true row hash = %s, want new-block", currentHash)
	}
}

// TestResolveBlockNumber_EmptyTable exercises bootstrap: no blocks yet,
// lastEmBlock=0. First call inserts at number=1.
func TestResolveBlockNumber_EmptyTable(t *testing.T) {
	pool := setupResolveDB(t)

	ix := newTestIndexer(pool, 0)

	got, err := ix.resolveBlockNumber("genesis", 1)
	if err != nil {
		t.Fatalf("resolveBlockNumber: %v", err)
	}
	if got != 1 {
		t.Errorf("got number %d, want 1", got)
	}
	if ix.lastEmBlock != 1 {
		t.Errorf("lastEmBlock = %d, want 1", ix.lastEmBlock)
	}
}

// TestResolveBlockNumber_IdempotentReprocessing exercises calling the
// function twice for the same block (e.g. a retry loop above us). The
// second call should adopt the first call's row, not double-insert and
// not advance lastEmBlock.
func TestResolveBlockNumber_IdempotentReprocessing(t *testing.T) {
	pool := setupResolveDB(t)
	ctx := context.Background()

	ix := newTestIndexer(pool, 0)

	first, err := ix.resolveBlockNumber("same-block", 1)
	if err != nil {
		t.Fatalf("first resolveBlockNumber: %v", err)
	}
	beforeSecond := ix.lastEmBlock

	second, err := ix.resolveBlockNumber("same-block", 1)
	if err != nil {
		t.Fatalf("second resolveBlockNumber: %v", err)
	}
	if first != second {
		t.Errorf("second call returned %d, want %d (same row)", second, first)
	}
	if ix.lastEmBlock != beforeSecond {
		t.Errorf("lastEmBlock advanced from %d to %d on idempotent call",
			beforeSecond, ix.lastEmBlock)
	}

	// Only one row in blocks for this hash.
	var cnt int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM blocks WHERE blockhash = 'same-block'`).Scan(&cnt); err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 1 {
		t.Errorf("blocks count for hash = %d, want 1", cnt)
	}
}
