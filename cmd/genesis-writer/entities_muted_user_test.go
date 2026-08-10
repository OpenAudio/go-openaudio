package main

import (
	"context"
	"os"
	"testing"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	etldb "github.com/OpenAudio/go-openaudio/pkg/etl/db"
	em "github.com/OpenAudio/go-openaudio/pkg/etl/processors/entity_manager"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// mutedUserSrcSchema is where this test's Discovery-Provider-shaped source
// tables live, kept separate from public so the ETL migrations do not touch it.
const mutedUserSrcSchema = "genesis_writer_mute_src_test"

// TestWriteMutedUsers_ReachesMutedUsersTable runs the writer's muted-user step
// against a source snapshot and replays what it emits through the real indexer
// handler, which is what the migration does.
//
// A handler-level test cannot catch the bug this guards. The writer emitted
// EntityType "MutedUser" while the indexer registers the mute handler under
// "User" (the key the SDK emits), so the dispatcher matched nothing and every
// muted-user row was dropped. Only replaying the writer's own transaction
// through a dispatcher — rather than a hand-built one — exercises that key.
func TestWriteMutedUsers_ReachesMutedUsersTable(t *testing.T) {
	dbURL := os.Getenv("ETL_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("ETL_TEST_DB_URL not set, skipping database test")
	}
	ctx := context.Background()
	logger := zap.NewNop()

	const (
		muterID     = int64(401)
		mutedID     = int64(402)
		unmutedID   = int64(403)
		strangerID  = int64(404)
		mutedAtYear = 2025
	)
	muterWallet := "0x3333333333333333333333333333333333333333"
	mutedWallet := "0x4444444444444444444444444444444444444444"
	unmutedWallet := "0x5555555555555555555555555555555555555555"
	mutedAt := time.Date(mutedAtYear, 6, 2, 11, 15, 0, 0, time.UTC)

	// ---- indexed state: what the earlier migration steps already wrote -------
	if err := etldb.RunMigrations(logger, dbURL, true); err != nil {
		t.Fatalf("run etl migrations: %v", err)
	}
	dst, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect etl db: %v", err)
	}
	defer dst.Close()

	exec := func(pool *pgxpool.Pool, sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}

	exec(dst, `DELETE FROM muted_users WHERE user_id = $1`, muterID)
	exec(dst, `DELETE FROM users WHERE user_id = ANY($1)`, []int64{muterID, mutedID, unmutedID})
	exec(dst, `INSERT INTO blocks (blockhash, parenthash, number) VALUES ('mute-test-block', '', 1)
		ON CONFLICT (blockhash) DO NOTHING`)
	exec(dst, `INSERT INTO users (user_id, handle, handle_lc, wallet, is_current, is_verified, is_deactivated, is_available, created_at, updated_at, txhash)
		VALUES ($1, 'muter', 'muter', $2, true, false, false, true, now(), now(), ''),
		       ($3, 'muted', 'muted', $4, true, false, false, true, now(), now(), ''),
		       ($5, 'unmuted', 'unmuted', $6, true, false, false, true, now(), now(), '')`,
		muterID, muterWallet, mutedID, mutedWallet, unmutedID, unmutedWallet)

	// ---- source snapshot: the DP columns the muted-user step reads ----------
	exec(dst, `DROP SCHEMA IF EXISTS `+mutedUserSrcSchema+` CASCADE`)
	exec(dst, `CREATE SCHEMA `+mutedUserSrcSchema)
	// Deferred rather than t.Cleanup so it runs before dst.Close above.
	defer func() {
		if _, err := dst.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+mutedUserSrcSchema+` CASCADE`); err != nil {
			t.Logf("drop source schema: %v", err)
		}
	}()

	srcCfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("parse source dsn: %v", err)
	}
	srcCfg.ConnConfig.RuntimeParams["search_path"] = mutedUserSrcSchema
	src, err := pgxpool.NewWithConfig(ctx, srcCfg)
	if err != nil {
		t.Fatalf("connect source schema: %v", err)
	}
	defer src.Close()

	exec(src, `CREATE TABLE users (user_id bigint, wallet text, is_current boolean)`)
	exec(src, `CREATE TABLE muted_users (user_id bigint, muted_user_id bigint, is_delete boolean, created_at timestamp)`)
	exec(src, `INSERT INTO users VALUES ($1, $2, true), ($3, $4, true), ($5, $6, true)`,
		muterID, muterWallet, mutedID, mutedWallet, unmutedID, unmutedWallet)
	exec(src, `INSERT INTO muted_users VALUES ($1, $2, false, $3)`, muterID, mutedID, mutedAt)
	// A mute that was later lifted. The step filters is_delete rows out, so it
	// must not produce a transaction.
	exec(src, `INSERT INTO muted_users VALUES ($1, $2, true, $3)`, muterID, unmutedID, mutedAt)
	// A mute of a user who is not in the source users table at all must not
	// produce a transaction either.
	exec(src, `INSERT INTO muted_users VALUES ($1, $2, false, $3)`, muterID, strangerID, mutedAt)

	// ---- run the writer step -------------------------------------------------
	w := newTestWriter(t, src)
	if err := w.writeMutedUsers(ctx); err != nil {
		t.Fatalf("writeMutedUsers: %v", err)
	}

	// The entity type asserted here is the contract: the SDK emits
	// EntityType.USER with Action.MUTE, and that is the only key the indexer's
	// mute handler answers to.
	mutes := decodeMigrationTxs(t, w.blockTxs, "User", "Mute")
	if len(mutes) != 1 {
		t.Fatalf("emitted %d User/Mute transactions, want 1 (found %d transactions in total)",
			len(mutes), len(w.blockTxs))
	}
	mute := mutes[0]

	if mute.GetUserId() != muterID {
		t.Errorf("user id = %d, want the muting user %d", mute.GetUserId(), muterID)
	}
	if mute.GetEntityId() != mutedID {
		t.Errorf("entity id = %d, want the muted user %d", mute.GetEntityId(), mutedID)
	}
	if mute.GetSigner() != muterWallet {
		t.Errorf("signer = %s, want the muting user's wallet %s", mute.GetSigner(), muterWallet)
	}

	// ---- replay it through the indexer --------------------------------------
	d := em.NewDispatcher(logger)
	d.Register(em.MuteUser())
	params := em.NewParams(&corev1.ManageEntityLegacy{
		UserId:     mute.GetUserId(),
		EntityType: mute.GetEntityType(),
		EntityId:   mute.GetEntityId(),
		Action:     mute.GetAction(),
		Metadata:   mute.GetMetadata(),
		Signature:  mute.GetSignature(),
		Signer:     mute.GetSigner(),
		Nonce:      mute.GetNonce(),
	}, 1, mutedAt, "blockhash", "txhash", dst, logger)
	if err := d.Dispatch(ctx, params); err != nil {
		t.Fatalf("indexing the emitted mute: %v", err)
	}

	var isDelete bool
	if err := dst.QueryRow(ctx,
		`SELECT is_delete FROM muted_users WHERE user_id = $1 AND muted_user_id = $2`,
		muterID, mutedID).Scan(&isDelete); err != nil {
		t.Fatalf("query muted_users: %v", err)
	}
	if isDelete {
		t.Error("is_delete = true, want an active mute")
	}
}
