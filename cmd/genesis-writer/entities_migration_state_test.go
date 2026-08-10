package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	etldb "github.com/OpenAudio/go-openaudio/pkg/etl/db"
	em "github.com/OpenAudio/go-openaudio/pkg/etl/processors/entity_manager"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// stateSrcSchema holds this test's Discovery-Provider-shaped source tables, kept
// apart from the pin test's schema so the two can run in the same database.
const stateSrcSchema = "genesis_writer_state_src_test"

// TestMigratedState_SurvivesWriterAndIndexer covers the four fields a snapshot
// migration could not carry, end to end: the writer reads them from a source
// snapshot and the real migration handlers index what it emits.
//
// Each of them is state a live client only ever produces after the entity
// exists -- three by editing a profile, one by accepting a manager invite -- so
// a create-only replay dropped them silently. Neither half of the fix is
// testable alone: emitting a field the indexer ignores changes nothing, and
// teaching the indexer to read a field the writer never sends changes nothing
// either. Measured on a production clone, the gap is 3,816 payout wallets, 425
// profile types, 96 coin flair mints and 493 approved manager grants.
func TestMigratedState_SurvivesWriterAndIndexer(t *testing.T) {
	dbURL := os.Getenv("ETL_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("ETL_TEST_DB_URL not set, skipping database test")
	}
	ctx := context.Background()
	logger := zap.NewNop()

	const (
		labelID   = int64(9101) // a label account with every profile field set
		plainID   = int64(9102) // an account with none of them
		managerID = int64(9103) // manages labelID through an approved grant
	)
	labelWallet := "0x9101000000000000000000000000000000000000"
	plainWallet := "0x9102000000000000000000000000000000000000"
	managerWallet := "0x9103000000000000000000000000000000000000"
	createdAt := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	payoutWallet := "So11111111111111111111111111111111111111112"
	flairMint := "CoinFlairMint11111111111111111111111111111"

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

	exec(dst, `INSERT INTO blocks (blockhash, parenthash, number) VALUES ('state-test-block', '', 1)
		ON CONFLICT (blockhash) DO NOTHING`)
	// Re-runnable: the migration handlers refuse to overwrite an existing row.
	exec(dst, `DELETE FROM users WHERE user_id = ANY($1)`, []int64{labelID, plainID, managerID})
	exec(dst, `DELETE FROM grants WHERE user_id = ANY($1)`, []int64{labelID, plainID, managerID})

	// ---- source snapshot -----------------------------------------------------
	exec(dst, `DROP SCHEMA IF EXISTS `+stateSrcSchema+` CASCADE`)
	exec(dst, `CREATE SCHEMA `+stateSrcSchema)
	defer func() {
		if _, err := dst.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+stateSrcSchema+` CASCADE`); err != nil {
			t.Logf("drop source schema: %v", err)
		}
	}()

	srcCfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("parse source dsn: %v", err)
	}
	srcCfg.ConnConfig.RuntimeParams["search_path"] = stateSrcSchema
	src, err := pgxpool.NewWithConfig(ctx, srcCfg)
	if err != nil {
		t.Fatalf("connect source schema: %v", err)
	}
	defer src.Close()

	exec(src, `CREATE TABLE users (
		user_id bigint, wallet text, handle text, name text, bio text, location text,
		profile_picture text, profile_picture_sizes text,
		cover_photo text, cover_photo_sizes text,
		twitter_handle text, instagram_handle text, website text,
		tiktok_handle text, donation text,
		is_verified boolean, is_deactivated boolean, is_available boolean,
		playlist_library jsonb, artist_pick_track_id bigint, allow_ai_attribution boolean,
		spl_usdc_payout_wallet text, profile_type text, coin_flair_mint text,
		created_at timestamp, is_current boolean)`)
	exec(src, `CREATE TABLE grants (
		grantee_address text, user_id bigint, created_at timestamp,
		is_revoked boolean, is_approved boolean, is_current boolean)`)

	insertSrcUser := func(id int64, wallet, handle string, payout, profileType, flair any) {
		exec(src, `INSERT INTO users (user_id, wallet, handle, is_verified, is_deactivated, is_available,
			allow_ai_attribution, spl_usdc_payout_wallet, profile_type, coin_flair_mint, created_at, is_current)
			VALUES ($1, $2, $3, false, false, true, false, $4, $5, $6, $7, true)`,
			id, wallet, handle, payout, profileType, flair, createdAt)
	}
	insertSrcUser(labelID, labelWallet, "label", payoutWallet, "label", flairMint)
	// The empty string is the source's other way of saying "unset" -- 376 rows
	// carry coin_flair_mint = '' -- and must land as NULL, not as ''.
	insertSrcUser(plainID, plainWallet, "plain", nil, nil, "")
	insertSrcUser(managerID, managerWallet, "manager", nil, nil, nil)

	// An approved user-to-user manager grant. The grantee is a wallet, not a
	// developer app, so the indexer's derivation gives NULL and the approval is
	// only recoverable from the source row.
	exec(src, `INSERT INTO grants VALUES ($1, $2, $3, false, true, true)`, managerWallet, labelID, createdAt)
	// A grant the source leaves NULL must stay NULL rather than becoming false.
	exec(src, `INSERT INTO grants VALUES ($1, $2, $3, false, NULL, true)`, managerWallet, plainID, createdAt)

	// ---- run the writer ------------------------------------------------------
	w := newTestWriter(t, src)
	if err := w.writeUsers(ctx); err != nil {
		t.Fatalf("writeUsers: %v", err)
	}
	if err := w.writeGrants(ctx); err != nil {
		t.Fatalf("writeGrants: %v", err)
	}

	// ---- writer half: the values have to be on the wire at all ---------------
	userTxs := decodeMigrationTxs(t, w.blockTxs, "User", "Create")
	if len(userTxs) != 3 {
		t.Fatalf("emitted %d User/Create transactions, want 3", len(userTxs))
	}
	for _, tx := range userTxs {
		var meta struct {
			Data struct {
				SplUsdcPayoutWallet string `json:"spl_usdc_payout_wallet"`
				ProfileType         string `json:"profile_type"`
				CoinFlairMint       string `json:"coin_flair_mint"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(tx.GetMetadata()), &meta); err != nil {
			t.Fatalf("unmarshal user metadata %q: %v", tx.GetMetadata(), err)
		}
		want := struct{ payout, profile, flair string }{}
		if tx.GetUserId() == labelID {
			want = struct{ payout, profile, flair string }{payoutWallet, "label", flairMint}
		}
		got := struct{ payout, profile, flair string }{
			meta.Data.SplUsdcPayoutWallet, meta.Data.ProfileType, meta.Data.CoinFlairMint,
		}
		if got != want {
			t.Errorf("user %d metadata = %+v, want %+v", tx.GetUserId(), got, want)
		}
	}

	grantTxs := decodeMigrationTxs(t, w.blockTxs, "Grant", "Create")
	if len(grantTxs) != 2 {
		t.Fatalf("emitted %d Grant/Create transactions, want 2", len(grantTxs))
	}
	for _, tx := range grantTxs {
		var meta struct {
			IsApproved *bool `json:"is_approved"`
		}
		if err := json.Unmarshal([]byte(tx.GetMetadata()), &meta); err != nil {
			t.Fatalf("unmarshal grant metadata %q: %v", tx.GetMetadata(), err)
		}
		switch tx.GetUserId() {
		case labelID:
			if meta.IsApproved == nil || !*meta.IsApproved {
				t.Errorf("approved grant metadata is_approved = %v, want true", meta.IsApproved)
			}
		case plainID:
			// Omitted, so the indexer falls back to its own derivation.
			if meta.IsApproved != nil {
				t.Errorf("null grant metadata is_approved = %v, want the key absent", *meta.IsApproved)
			}
		}
	}

	// ---- indexer half: replay through the real migration handlers ------------
	d := em.NewDispatcher(logger)
	em.RegisterMigrationOverrides(d)
	for _, tx := range append(append([]*corev1.ManageEntityLegacyMigration{}, userTxs...), grantTxs...) {
		params := em.NewParams(&corev1.ManageEntityLegacy{
			UserId:     tx.GetUserId(),
			EntityType: tx.GetEntityType(),
			EntityId:   tx.GetEntityId(),
			Action:     tx.GetAction(),
			Metadata:   tx.GetMetadata(),
			Signature:  tx.GetSignature(),
			Signer:     tx.GetSigner(),
			Nonce:      tx.GetNonce(),
		}, 1, createdAt, "state-test-block", "state-test-tx", dst, logger)
		if err := d.Dispatch(ctx, params); err != nil {
			t.Fatalf("indexing %s/%s for user %d: %v", tx.GetEntityType(), tx.GetAction(), tx.GetUserId(), err)
		}
	}

	// ---- the indexed rows ----------------------------------------------------
	for _, tc := range []struct {
		userID                          int64
		wantPayout, wantType, wantFlair *string
	}{
		{labelID, &payoutWallet, ptr("label"), &flairMint},
		{plainID, nil, nil, nil},
	} {
		var payout, profileType, flair *string
		if err := dst.QueryRow(ctx,
			`SELECT spl_usdc_payout_wallet, profile_type::text, coin_flair_mint
			 FROM users WHERE user_id = $1 AND is_current = true`, tc.userID).
			Scan(&payout, &profileType, &flair); err != nil {
			t.Fatalf("query user %d: %v", tc.userID, err)
		}
		for _, f := range []struct {
			col       string
			got, want *string
		}{
			{"spl_usdc_payout_wallet", payout, tc.wantPayout},
			{"profile_type", profileType, tc.wantType},
			{"coin_flair_mint", flair, tc.wantFlair},
		} {
			if !strPtrEqual(f.got, f.want) {
				t.Errorf("user %d %s = %s, want %s", tc.userID, f.col, showStrPtr(f.got), showStrPtr(f.want))
			}
		}
	}

	for _, tc := range []struct {
		grantorID int64
		want      *bool
	}{
		{labelID, ptr(true)},
		{plainID, nil},
	} {
		var approved *bool
		if err := dst.QueryRow(ctx,
			`SELECT is_approved FROM grants WHERE user_id = $1 AND grantee_address = $2 AND is_current = true`,
			tc.grantorID, managerWallet).Scan(&approved); err != nil {
			t.Fatalf("query grant from user %d: %v", tc.grantorID, err)
		}
		switch {
		case tc.want == nil && approved != nil:
			t.Errorf("grant from user %d: is_approved = %v, want NULL", tc.grantorID, *approved)
		case tc.want != nil && (approved == nil || *approved != *tc.want):
			t.Errorf("grant from user %d: is_approved = %v, want %v", tc.grantorID, approved, *tc.want)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func strPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func showStrPtr(p *string) string {
	if p == nil {
		return "NULL"
	}
	return *p
}
