package main

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	etldb "github.com/OpenAudio/go-openaudio/pkg/etl/db"
	em "github.com/OpenAudio/go-openaudio/pkg/etl/processors/entity_manager"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// srcSchema is where the test's Discovery-Provider-shaped source tables live. It
// is a schema rather than a database so the test needs no CREATE DATABASE right,
// and it is separate from public so the ETL migrations do not touch it.
const srcSchema = "genesis_writer_pin_src_test"

// TestWriteCommentPins_SetsPinnedCommentID runs the writer's pin step against a
// source snapshot and replays what it emits through the real indexer handler,
// which is what the migration will do. Anything less would not have caught the
// bug this step fixes: the writer emitted no Comment/Pin transaction at all, so
// every handler-level test of pinning passed while 1,286 tracks arrived with a
// NULL pinned_comment_id.
//
// The ETL side is the production commentPinHandler, unmodified — Comment/Pin has
// no migration override — so the transaction has to satisfy its real validation:
// the comment must exist and the signer must be the track owner.
func TestWriteCommentPins_SetsPinnedCommentID(t *testing.T) {
	dbURL := os.Getenv("ETL_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("ETL_TEST_DB_URL not set, skipping database test")
	}
	ctx := context.Background()
	logger := zap.NewNop()

	const (
		ownerID    = int64(101)
		strangerID = int64(102)
		trackID    = int64(2001)
		commentID  = int64(3001)
	)
	ownerWallet := "0x1111111111111111111111111111111111111111"
	strangerWallet := "0x2222222222222222222222222222222222222222"
	// The pin carries no timestamp of its own in the source, so the writer sends
	// the track's updated_at and the indexed row must land on it.
	trackUpdatedAt := time.Date(2025, 4, 9, 17, 30, 0, 0, time.UTC)

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

	exec(dst, `INSERT INTO blocks (blockhash, parenthash, number) VALUES ('pin-test-block', '', 1)
		ON CONFLICT (blockhash) DO NOTHING`)
	exec(dst, `INSERT INTO users (user_id, handle, handle_lc, wallet, is_current, is_verified, is_deactivated, is_available, created_at, updated_at, txhash)
		VALUES ($1, 'owner', 'owner', $2, true, false, false, true, now(), now(), ''),
		       ($3, 'stranger', 'stranger', $4, true, false, false, true, now(), now(), '')`,
		ownerID, ownerWallet, strangerID, strangerWallet)
	exec(dst, `INSERT INTO tracks (track_id, owner_id, title, is_current, is_delete, track_segments, created_at, updated_at, txhash)
		VALUES ($1, $2, 'Pinned', true, false, '[]', now(), now(), '')`, trackID, ownerID)
	// A comment written by someone other than the track owner, which is the
	// normal case: an artist pins a listener's comment.
	exec(dst, `INSERT INTO comments (comment_id, text, user_id, entity_id, entity_type, created_at, updated_at, is_delete, is_visible, is_edited, txhash, blockhash, blocknumber)
		VALUES ($1, 'pin me', $2, $3, 'Track', now(), now(), false, true, false, '', '', 1)`,
		commentID, strangerID, trackID)

	// ---- source snapshot: the DP columns the pin step reads -----------------
	exec(dst, `DROP SCHEMA IF EXISTS `+srcSchema+` CASCADE`)
	exec(dst, `CREATE SCHEMA `+srcSchema)
	// Deferred rather than t.Cleanup so it runs before dst.Close above.
	defer func() {
		if _, err := dst.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+srcSchema+` CASCADE`); err != nil {
			t.Logf("drop source schema: %v", err)
		}
	}()

	srcCfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("parse source dsn: %v", err)
	}
	srcCfg.ConnConfig.RuntimeParams["search_path"] = srcSchema
	src, err := pgxpool.NewWithConfig(ctx, srcCfg)
	if err != nil {
		t.Fatalf("connect source schema: %v", err)
	}
	defer src.Close()

	exec(src, `CREATE TABLE users (user_id bigint, wallet text, is_current boolean)`)
	// updated_at is `timestamp without time zone`, as it is in the DP schema:
	// the column type decides whether the writer reads the value back in UTC.
	exec(src, `CREATE TABLE tracks (track_id bigint, owner_id bigint, pinned_comment_id bigint, is_current boolean, updated_at timestamp)`)
	exec(src, `CREATE TABLE comments (comment_id bigint, user_id bigint)`)
	exec(src, `INSERT INTO users VALUES ($1, $2, true), ($3, $4, true)`,
		ownerID, ownerWallet, strangerID, strangerWallet)
	exec(src, `INSERT INTO tracks VALUES ($1, $2, $3, true, $4)`, trackID, ownerID, commentID, trackUpdatedAt)
	// An unpinned track must not produce a transaction.
	exec(src, `INSERT INTO tracks VALUES ($1, $2, NULL, true, $3)`, trackID+1, ownerID, trackUpdatedAt)
	exec(src, `INSERT INTO comments VALUES ($1, $2)`, commentID, strangerID)

	// ---- run the writer step -------------------------------------------------
	w := newTestWriter(t, src)
	if err := w.writeCommentPins(ctx); err != nil {
		t.Fatalf("writeCommentPins: %v", err)
	}

	pins := decodeMigrationTxs(t, w.blockTxs, "Comment", "Pin")
	if len(pins) != 1 {
		t.Fatalf("emitted %d Comment/Pin transactions, want 1 (the unpinned track must not produce one)", len(pins))
	}
	pin := pins[0]

	if pin.GetEntityId() != commentID {
		t.Errorf("entity id = %d, want the comment %d", pin.GetEntityId(), commentID)
	}
	if pin.GetUserId() != ownerID {
		t.Errorf("user id = %d, want the track owner %d", pin.GetUserId(), ownerID)
	}
	if pin.GetSigner() != ownerWallet {
		t.Errorf("signer = %s, want the track owner's wallet %s", pin.GetSigner(), ownerWallet)
	}

	var meta struct {
		EntityID  int64  `json:"entity_id"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal([]byte(pin.GetMetadata()), &meta); err != nil {
		t.Fatalf("unmarshal pin metadata %q: %v", pin.GetMetadata(), err)
	}
	if meta.EntityID != trackID {
		t.Errorf("metadata entity_id = %d, want the track %d", meta.EntityID, trackID)
	}
	blockTime, err := time.Parse(time.RFC3339, meta.CreatedAt)
	if err != nil {
		t.Fatalf("parse metadata created_at %q: %v", meta.CreatedAt, err)
	}
	if !blockTime.Equal(trackUpdatedAt) {
		t.Errorf("metadata created_at = %s, want the source track's updated_at %s", blockTime, trackUpdatedAt)
	}

	// ---- replay it through the indexer --------------------------------------
	// The indexer takes the block time for a migration transaction from the
	// metadata's created_at, so the handler sees the timestamp asserted above.
	d := em.NewDispatcher(logger)
	d.Register(em.CommentPin())
	params := em.NewParams(&corev1.ManageEntityLegacy{
		UserId:     pin.GetUserId(),
		EntityType: pin.GetEntityType(),
		EntityId:   pin.GetEntityId(),
		Action:     pin.GetAction(),
		Metadata:   pin.GetMetadata(),
		Signature:  pin.GetSignature(),
		Signer:     pin.GetSigner(),
		Nonce:      pin.GetNonce(),
	}, 1, blockTime, "blockhash", "txhash", dst, logger)
	if err := d.Dispatch(ctx, params); err != nil {
		t.Fatalf("indexing the emitted pin: %v", err)
	}

	var pinnedID *int64
	var updatedAt time.Time
	if err := dst.QueryRow(ctx,
		`SELECT pinned_comment_id, updated_at FROM tracks WHERE track_id = $1 AND is_current = true`,
		trackID).Scan(&pinnedID, &updatedAt); err != nil {
		t.Fatalf("query track: %v", err)
	}
	if pinnedID == nil || *pinnedID != commentID {
		t.Errorf("pinned_comment_id = %v, want %d", pinnedID, commentID)
	}
	if !updatedAt.Equal(trackUpdatedAt) {
		t.Errorf("tracks.updated_at = %s, want the source's %s", updatedAt.UTC(), trackUpdatedAt)
	}
}

// newTestWriter builds a Writer that signs and buffers transactions without a
// chain: MaxTxsPerBlock is high enough that no block is ever flushed, so
// blockTxs holds everything the step emitted and no destination database or
// blockstore is needed.
func newTestWriter(t *testing.T, src *pgxpool.Pool) *Writer {
	t.Helper()
	privKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return &Writer{
		cfg:         &WriterConfig{MaxTxsPerBlock: 1 << 20, BatchSize: 64},
		srcDB:       src,
		sigCfg:      signingConfig("dev"),
		privKey:     privKey,
		signerAddr:  crypto.PubkeyToAddress(privKey.PublicKey).Hex(),
		logger:      zap.NewNop(),
		marshalPool: sync.Pool{New: func() any { return proto.MarshalOptions{} }},
	}
}

// decodeMigrationTxs returns the buffered transactions matching an entity type
// and action.
func decodeMigrationTxs(t *testing.T, txs [][]byte, entityType, action string) []*corev1.ManageEntityLegacyMigration {
	t.Helper()
	var out []*corev1.ManageEntityLegacyMigration
	for i, raw := range txs {
		var stx corev1.SignedTransaction
		if err := proto.Unmarshal(raw, &stx); err != nil {
			t.Fatalf("unmarshal tx %d: %v", i, err)
		}
		me := stx.GetManageEntityMigration()
		if me == nil {
			continue
		}
		if me.GetEntityType() == entityType && me.GetAction() == action {
			out = append(out, me)
		}
	}
	return out
}
