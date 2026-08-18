package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const grantOrderSrcSchema = "genesis_writer_grant_order_test"

// TestWriteGrants_NewestGrantRowWins pins the de-duplication in the grants query.
//
// A grant that was granted, revoked, then re-granted leaves TWO is_current rows
// for one (user_id, grantee_address): one revoked, one not. Emitting both puts
// two Creates in flight for the same authorization, and the auth projection
// takes the first and declines the rest -- so whichever landed first decided
// whether the manager kept access.
//
// An ORDER BY cannot fix this: processBatched emits each batch from NumCPU
// goroutines, so emission order is a race no query can constrain. Only one row
// may be emitted per pair.
//
// On the 2026-08-16 snapshot this affected 2 of 4,330 current grants. It landed
// correctly by luck; a different plan would have silently revoked them, and
// nothing surfaces it but a warning line.
func TestWriteGrants_NewestGrantRowWins(t *testing.T) {
	dbURL := os.Getenv("ETL_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("ETL_TEST_DB_URL not set, skipping database test")
	}
	ctx := context.Background()

	const grantorID = int64(9101)
	grantorWallet := "0x7777777777777777777777777777777777777777"
	grantee := "0x8888888888888888888888888888888888888888"
	granted := time.Date(2026, 3, 25, 4, 37, 52, 0, time.UTC)
	regranted := time.Date(2026, 7, 10, 5, 15, 54, 0, time.UTC)

	dst, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	defer dst.Close()

	exec := func(pool *pgxpool.Pool, sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}

	exec(dst, `DROP SCHEMA IF EXISTS `+grantOrderSrcSchema+` CASCADE`)
	exec(dst, `CREATE SCHEMA `+grantOrderSrcSchema)
	defer func() {
		if _, err := dst.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+grantOrderSrcSchema+` CASCADE`); err != nil {
			t.Logf("drop source schema: %v", err)
		}
	}()

	srcCfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("parse source dsn: %v", err)
	}
	srcCfg.ConnConfig.RuntimeParams["search_path"] = grantOrderSrcSchema
	src, err := pgxpool.NewWithConfig(ctx, srcCfg)
	if err != nil {
		t.Fatalf("connect source schema: %v", err)
	}
	defer src.Close()

	exec(src, `CREATE TABLE users (user_id bigint, wallet text, is_current boolean)`)
	exec(src, `CREATE TABLE grants (user_id bigint, grantee_address text, is_current boolean,
		is_revoked boolean, is_approved boolean, created_at timestamp, updated_at timestamp)`)
	exec(src, `INSERT INTO users VALUES ($1, $2, true)`, grantorID, grantorWallet)

	// Inserted revoked-first so a query without the tiebreaker is likely to
	// return it first — the failure this test exists to catch.
	exec(src, `INSERT INTO grants VALUES ($1, $2, true, true, true, $3, $4)`,
		grantorID, grantee, granted, regranted)
	exec(src, `INSERT INTO grants VALUES ($1, $2, true, false, true, $3, $4)`,
		grantorID, grantee, regranted, regranted)

	w := newTestWriter(t, src)
	if err := w.writeGrants(ctx); err != nil {
		t.Fatalf("writeGrants: %v", err)
	}

	grants := decodeMigrationTxs(t, w.blockTxs, "Grant", "Create")
	if len(grants) != 1 {
		t.Fatalf("emitted %d Grant/Create transactions for one (user, grantee), want exactly 1 -- "+
			"emitting both racing Creates is what silently revoked managers", len(grants))
	}

	// The surviving row must be the newest (the re-grant), not the revoke.
	if got := grants[0].GetMetadata(); !stringContains(got, `"is_revoked":false`) {
		t.Errorf("emitted grant metadata = %s\nwant the newest, non-revoked row", got)
	}
}


func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
