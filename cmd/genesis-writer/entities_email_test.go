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

// emailSrcSchema is where this test's Discovery-Provider-shaped source tables
// live. It is a schema rather than a database so the test needs no CREATE
// DATABASE right, and it is separate from public so the ETL migrations do not
// touch it.
const emailSrcSchema = "genesis_writer_email_src_test"

// TestWriteEncryptedEmails_IndexesWithGrants runs the writer's encrypted-email
// step against a source snapshot and replays what it emits through the real
// indexer handler, which is what the migration will do.
//
// Only that combination catches the bug this fixes. encryptedEmailHandler takes
// the owner from the metadata key email_owner_user_id, not from the
// transaction's user_id, and the writer never emitted that key — so every
// handler-level email test passed while all 4,218 encrypted emails and their
// 9,282 access grants were rejected on the way in.
//
// EncryptedEmail has no migration override, so the production handler's real
// validation applies verbatim.
func TestWriteEncryptedEmails_IndexesWithGrants(t *testing.T) {
	dbURL := os.Getenv("ETL_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("ETL_TEST_DB_URL not set, skipping database test")
	}
	ctx := context.Background()
	logger := zap.NewNop()

	const (
		ownerID    = int64(401)
		receiverID = int64(402)
		// An owner with no wallet: the row cannot be signed as its owner, so the
		// step must skip it rather than emit a transaction that cannot be
		// attributed.
		walletlessID = int64(403)
	)
	ownerWallet := "0x4111111111111111111111111111111111111111"
	receiverWallet := "0x4222222222222222222222222222222222222222"
	emailCreatedAt := time.Date(2025, 3, 2, 11, 15, 0, 0, time.UTC)

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

	// ---- indexed state: what the earlier migration steps already wrote -------
	exec(dst, `DELETE FROM email_access WHERE email_owner_user_id = $1`, ownerID)
	exec(dst, `DELETE FROM encrypted_emails WHERE email_owner_user_id = $1`, ownerID)
	exec(dst, `INSERT INTO blocks (blockhash, parenthash, number) VALUES ('email-test-block', '', 1)
		ON CONFLICT (blockhash) DO NOTHING`)
	exec(dst, `INSERT INTO users (user_id, handle, handle_lc, wallet, is_current, is_verified, is_deactivated, is_available, created_at, updated_at, txhash)
		VALUES ($1, 'emailowner', 'emailowner', $2, true, false, false, true, now(), now(), ''),
		       ($3, 'emailreceiver', 'emailreceiver', $4, true, false, false, true, now(), now(), '')
		ON CONFLICT DO NOTHING`,
		ownerID, ownerWallet, receiverID, receiverWallet)

	// ---- source snapshot: the DP tables the email step reads ----------------
	exec(dst, `DROP SCHEMA IF EXISTS `+emailSrcSchema+` CASCADE`)
	exec(dst, `CREATE SCHEMA `+emailSrcSchema)
	// Deferred rather than t.Cleanup so it runs before dst.Close above.
	defer func() {
		if _, err := dst.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+emailSrcSchema+` CASCADE`); err != nil {
			t.Logf("drop source schema: %v", err)
		}
	}()

	srcCfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("parse source dsn: %v", err)
	}
	srcCfg.ConnConfig.RuntimeParams["search_path"] = emailSrcSchema
	src, err := pgxpool.NewWithConfig(ctx, srcCfg)
	if err != nil {
		t.Fatalf("connect source schema: %v", err)
	}
	defer src.Close()

	exec(src, `CREATE TABLE users (user_id bigint, wallet text, is_current boolean)`)
	exec(src, `CREATE TABLE encrypted_emails (email_owner_user_id bigint, encrypted_email text, created_at timestamptz)`)
	exec(src, `CREATE TABLE email_access (email_owner_user_id bigint, receiving_user_id bigint, grantor_user_id bigint, encrypted_key text, created_at timestamptz)`)
	exec(src, `INSERT INTO users VALUES ($1, $2, true), ($3, $4, true), ($5, '', true)`,
		ownerID, ownerWallet, receiverID, receiverWallet, walletlessID)
	exec(src, `INSERT INTO encrypted_emails VALUES ($1, 'cipher-text', $2), ($3, 'orphan-cipher', $2)`,
		ownerID, emailCreatedAt, walletlessID)
	// Two grants: the owner's own initial grant and one to another user.
	exec(src, `INSERT INTO email_access VALUES ($1, $1, $1, 'key-self', $3), ($1, $2, $1, 'key-receiver', $3)`,
		ownerID, receiverID, emailCreatedAt)

	// ---- run the writer step -------------------------------------------------
	w := newTestWriter(t, src)
	if err := w.writeEncryptedEmails(ctx); err != nil {
		t.Fatalf("writeEncryptedEmails: %v", err)
	}

	emails := decodeMigrationTxs(t, w.blockTxs, "EncryptedEmail", "AddEmail")
	if len(emails) != 1 {
		t.Fatalf("emitted %d EncryptedEmail/AddEmail transactions, want 1 (the walletless owner must not produce one)", len(emails))
	}
	email := emails[0]

	if email.GetUserId() != ownerID {
		t.Errorf("user id = %d, want the email owner %d", email.GetUserId(), ownerID)
	}
	if email.GetSigner() != ownerWallet {
		t.Errorf("signer = %s, want the owner's wallet %s", email.GetSigner(), ownerWallet)
	}

	var meta struct {
		EmailOwnerUserID int64  `json:"email_owner_user_id"`
		EncryptedEmail   string `json:"encrypted_email"`
		AccessGrants     []struct {
			ReceivingUserID int64  `json:"receiving_user_id"`
			GrantorUserID   int64  `json:"grantor_user_id"`
			EncryptedKey    string `json:"encrypted_key"`
		} `json:"access_grants"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal([]byte(email.GetMetadata()), &meta); err != nil {
		t.Fatalf("unmarshal email metadata %q: %v", email.GetMetadata(), err)
	}
	// The handler reads the owner from metadata, not from the transaction's
	// user_id, and rejects the transaction when the key is absent.
	if meta.EmailOwnerUserID != ownerID {
		t.Errorf("metadata email_owner_user_id = %d, want %d", meta.EmailOwnerUserID, ownerID)
	}
	if meta.EncryptedEmail != "cipher-text" {
		t.Errorf("metadata encrypted_email = %q", meta.EncryptedEmail)
	}
	if len(meta.AccessGrants) != 2 {
		t.Fatalf("metadata carries %d access grants, want 2", len(meta.AccessGrants))
	}
	blockTime, err := time.Parse(time.RFC3339, meta.CreatedAt)
	if err != nil {
		t.Fatalf("parse metadata created_at %q: %v", meta.CreatedAt, err)
	}

	// ---- replay it through the indexer --------------------------------------
	d := em.NewDispatcher(logger)
	d.Register(em.EncryptedEmailCreate())
	params := em.NewParams(&corev1.ManageEntityLegacy{
		UserId:     email.GetUserId(),
		EntityType: email.GetEntityType(),
		EntityId:   email.GetEntityId(),
		Action:     email.GetAction(),
		Metadata:   email.GetMetadata(),
		Signature:  email.GetSignature(),
		Signer:     email.GetSigner(),
		Nonce:      email.GetNonce(),
	}, 1, blockTime, "email-test-block", "email-txhash", dst, logger)
	if err := d.Dispatch(ctx, params); err != nil {
		t.Fatalf("indexing the emitted encrypted email: %v", err)
	}

	var encrypted string
	if err := dst.QueryRow(ctx,
		`SELECT encrypted_email FROM encrypted_emails WHERE email_owner_user_id = $1`, ownerID).Scan(&encrypted); err != nil {
		t.Fatalf("query encrypted_emails: %v", err)
	}
	if encrypted != "cipher-text" {
		t.Errorf("indexed encrypted_email = %q, want %q", encrypted, "cipher-text")
	}

	rows, err := dst.Query(ctx,
		`SELECT receiving_user_id, grantor_user_id, encrypted_key FROM email_access
		WHERE email_owner_user_id = $1 ORDER BY receiving_user_id`, ownerID)
	if err != nil {
		t.Fatalf("query email_access: %v", err)
	}
	defer rows.Close()
	type grant struct {
		receiving, grantor int64
		key                string
	}
	var got []grant
	for rows.Next() {
		var g grant
		if err := rows.Scan(&g.receiving, &g.grantor, &g.key); err != nil {
			t.Fatalf("scan email_access: %v", err)
		}
		got = append(got, g)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("email_access rows: %v", err)
	}
	want := []grant{
		{receiving: ownerID, grantor: ownerID, key: "key-self"},
		{receiving: receiverID, grantor: ownerID, key: "key-receiver"},
	}
	if len(got) != len(want) {
		t.Fatalf("indexed %d access grants, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("access grant %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestWriteEmailAccess_EmitsHandlerShape covers the orphan-grant step: grants
// whose owner has no encrypted_emails row. It asserts the routing key and
// metadata shape emailAccessHandler is registered for — EmailAccess/Update with
// grants nested under access_grants — because the writer previously sent
// EmailAccess/Create with the grant fields at the top level, which no handler is
// registered for and which would therefore have been dropped without a trace.
//
// It stops at the emitted transaction rather than replaying it: the handler also
// requires the grantor to already hold access to the email, which an orphan's
// first grant cannot satisfy. The production snapshot has no orphan grants, so
// this step emits nothing there.
func TestWriteEmailAccess_EmitsHandlerShape(t *testing.T) {
	dbURL := os.Getenv("ETL_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("ETL_TEST_DB_URL not set, skipping database test")
	}
	ctx := context.Background()

	const (
		ownerID    = int64(501)
		receiverID = int64(502)
	)
	ownerWallet := "0x5111111111111111111111111111111111111111"
	grantCreatedAt := time.Date(2025, 5, 6, 9, 0, 0, 0, time.UTC)

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect etl db: %v", err)
	}
	defer pool.Close()

	schema := emailSrcSchema + "_orphan"
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
		t.Fatalf("drop source schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create source schema: %v", err)
	}
	defer func() {
		if _, err := pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Logf("drop source schema: %v", err)
		}
	}()

	srcCfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("parse source dsn: %v", err)
	}
	srcCfg.ConnConfig.RuntimeParams["search_path"] = schema
	src, err := pgxpool.NewWithConfig(ctx, srcCfg)
	if err != nil {
		t.Fatalf("connect source schema: %v", err)
	}
	defer src.Close()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := src.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	exec(`CREATE TABLE users (user_id bigint, wallet text, is_current boolean)`)
	exec(`CREATE TABLE encrypted_emails (email_owner_user_id bigint)`)
	exec(`CREATE TABLE email_access (email_owner_user_id bigint, receiving_user_id bigint, grantor_user_id bigint, encrypted_key text, created_at timestamptz)`)
	exec(`INSERT INTO users VALUES ($1, $2, true)`, ownerID, ownerWallet)
	exec(`INSERT INTO email_access VALUES ($1, $2, $1, 'orphan-key', $3)`, ownerID, receiverID, grantCreatedAt)

	w := newTestWriter(t, src)
	if err := w.writeEmailAccess(ctx); err != nil {
		t.Fatalf("writeEmailAccess: %v", err)
	}

	grants := decodeMigrationTxs(t, w.blockTxs, "EmailAccess", "Update")
	if len(grants) != 1 {
		t.Fatalf("emitted %d EmailAccess/Update transactions, want 1", len(grants))
	}
	g := grants[0]
	if g.GetSigner() != ownerWallet {
		t.Errorf("signer = %s, want the owner's wallet %s", g.GetSigner(), ownerWallet)
	}

	var meta struct {
		EmailOwnerUserID int64 `json:"email_owner_user_id"`
		AccessGrants     []struct {
			ReceivingUserID int64  `json:"receiving_user_id"`
			GrantorUserID   int64  `json:"grantor_user_id"`
			EncryptedKey    string `json:"encrypted_key"`
		} `json:"access_grants"`
	}
	if err := json.Unmarshal([]byte(g.GetMetadata()), &meta); err != nil {
		t.Fatalf("unmarshal email access metadata %q: %v", g.GetMetadata(), err)
	}
	if meta.EmailOwnerUserID != ownerID {
		t.Errorf("metadata email_owner_user_id = %d, want %d", meta.EmailOwnerUserID, ownerID)
	}
	if len(meta.AccessGrants) != 1 {
		t.Fatalf("metadata carries %d access grants, want 1", len(meta.AccessGrants))
	}
	if meta.AccessGrants[0].ReceivingUserID != receiverID ||
		meta.AccessGrants[0].GrantorUserID != ownerID ||
		meta.AccessGrants[0].EncryptedKey != "orphan-key" {
		t.Errorf("access grant = %+v", meta.AccessGrants[0])
	}
}
