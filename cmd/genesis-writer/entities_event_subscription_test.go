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

// eventSubSrcSchema is where this test's Discovery-Provider-shaped source tables
// live. A schema rather than a database so the test needs no CREATE DATABASE
// right, and separate from public so the ETL migrations do not touch it.
const eventSubSrcSchema = "genesis_writer_event_sub_src_test"

// TestWriteEventSubscriptions_PreservesEntityTypeAndID runs the writer's event
// subscription step against a source snapshot and replays what it emits through
// the real indexer handler, which is what the migration will do.
//
// The bug this covers: `subscriptions` holds both user subscriptions and
// subscriptions to an event, distinguished by entity_type, and for an Event row
// the user_id column is overloaded with the event id. writeSubscriptions
// hardcodes EntityType "User" and joins the target id against `users`, so on a
// production clone all 606 Event rows failed the join and no transaction was
// emitted for them at all. Only a test that starts from the source table and
// checks the emitted transaction can catch that: every handler-level test of
// Subscribe/Event passed while the migration carried none of them.
//
// The ETL side is the production Subscribe handler under its migration override,
// unmodified, so the transaction has to satisfy real validation: the target
// event must exist and the signer must be the subscriber.
func TestWriteEventSubscriptions_PreservesEntityTypeAndID(t *testing.T) {
	dbURL := os.Getenv("ETL_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("ETL_TEST_DB_URL not set, skipping database test")
	}
	ctx := context.Background()
	logger := zap.NewNop()

	const (
		subscriberID = int64(9101)
		hostID       = int64(9102)
		publisherID  = int64(9103)
		// Event ids and user ids share a numeric space in the source; these are
		// deliberately outside the seeded user ids so a `users` join cannot
		// accidentally rescue them.
		eventID        = int64(950001)
		deletedEventID = int64(950002)
	)
	subscriberWallet := "0x3333333333333333333333333333333333333333"
	hostWallet := "0x4444444444444444444444444444444444444444"
	publisherWallet := "0x5555555555555555555555555555555555555555"
	subscribedAt := time.Date(2025, 6, 2, 11, 15, 0, 0, time.UTC)

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

	exec(dst, `INSERT INTO blocks (blockhash, parenthash, number) VALUES ('event-sub-test-block', '', 1)
		ON CONFLICT (blockhash) DO NOTHING`)
	// Reset any state left by a previous run so the test is re-runnable against
	// a persistent database.
	exec(dst, `DELETE FROM subscriptions WHERE subscriber_id = $1`, subscriberID)
	exec(dst, `DELETE FROM events WHERE event_id = ANY($1)`, []int64{eventID, deletedEventID})
	exec(dst, `DELETE FROM users WHERE user_id = ANY($1)`, []int64{subscriberID, hostID, publisherID})

	exec(dst, `INSERT INTO users (user_id, handle, handle_lc, wallet, is_current, is_verified, is_deactivated, is_available, created_at, updated_at, txhash)
		VALUES ($1, 'subscriber9101', 'subscriber9101', $2, true, false, false, true, now(), now(), ''),
		       ($3, 'host9102', 'host9102', $4, true, false, false, true, now(), now(), ''),
		       ($5, 'publisher9103', 'publisher9103', $6, true, false, false, true, now(), now(), '')`,
		subscriberID, subscriberWallet, hostID, hostWallet, publisherID, publisherWallet)
	seedIndexedEvent := func(id, owner int64) {
		exec(dst, `INSERT INTO events (
				event_id, event_type, user_id, entity_type, entity_id,
				end_date, event_data, is_deleted,
				created_at, updated_at, txhash, blockhash, blocknumber
			) VALUES ($1, 'remix_contest', $2, 'track', 1, now() + interval '1 day', '{}', false,
				now(), now(), '', 'event-sub-test-block', 1)`, id, owner)
	}
	seedIndexedEvent(eventID, hostID)
	seedIndexedEvent(deletedEventID, hostID)

	// ---- source snapshot: the DP columns the step reads ----------------------
	exec(dst, `DROP SCHEMA IF EXISTS `+eventSubSrcSchema+` CASCADE`)
	exec(dst, `CREATE SCHEMA `+eventSubSrcSchema)
	// Deferred rather than t.Cleanup so it runs before dst.Close above.
	defer func() {
		if _, err := dst.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+eventSubSrcSchema+` CASCADE`); err != nil {
			t.Logf("drop source schema: %v", err)
		}
	}()

	srcCfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("parse source dsn: %v", err)
	}
	srcCfg.ConnConfig.RuntimeParams["search_path"] = eventSubSrcSchema
	src, err := pgxpool.NewWithConfig(ctx, srcCfg)
	if err != nil {
		t.Fatalf("connect source schema: %v", err)
	}
	defer src.Close()

	exec(src, `CREATE TABLE users (user_id bigint, wallet text, is_current boolean)`)
	exec(src, `CREATE TABLE events (event_id bigint, is_deleted boolean)`)
	// entity_id is `integer` and created_at `timestamp without time zone`, as
	// they are in the DP schema.
	exec(src, `CREATE TABLE subscriptions (
		subscriber_id bigint, user_id bigint, entity_type text, entity_id integer,
		is_current boolean, is_delete boolean, created_at timestamp)`)

	exec(src, `INSERT INTO users VALUES ($1, $2, true), ($3, $4, true), ($5, $6, true)`,
		subscriberID, subscriberWallet, hostID, hostWallet, publisherID, publisherWallet)
	exec(src, `INSERT INTO events VALUES ($1, false), ($2, false)`, eventID, deletedEventID)

	srcSub := func(targetID int64, entityType string, isDelete bool) {
		t.Helper()
		// user_id is overloaded with the event id for an Event row, exactly as
		// the source stores it, and entity_id carries the same value.
		exec(src, `INSERT INTO subscriptions
			(subscriber_id, user_id, entity_type, entity_id, is_current, is_delete, created_at)
			VALUES ($1, $2, $3, $4, true, $5, $6)`,
			subscriberID, targetID, entityType, int32(targetID), isDelete, subscribedAt)
	}
	// The row under test.
	srcSub(eventID, "Event", false)
	// A soft-deleted event subscription: migrated too, as one transaction.
	srcSub(deletedEventID, "Event", true)
	// A user subscription must not be picked up by this step.
	srcSub(publisherID, "User", false)

	// ---- run the writer step -------------------------------------------------
	w := newTestWriter(t, src)
	if err := w.writeEventSubscriptions(ctx); err != nil {
		t.Fatalf("writeEventSubscriptions: %v", err)
	}

	subs := decodeMigrationTxs(t, w.blockTxs, "Event", "Subscribe")
	byEntity := map[int64]*corev1.ManageEntityLegacyMigration{}
	for _, s := range subs {
		byEntity[s.GetEntityId()] = s
	}

	// The subscription must survive as (entity type Event, entity id = the
	// event). Both halves matter: an entity id with the wrong type is indexed
	// against the wrong table, and no transaction at all is silent data loss.
	sub, ok := byEntity[eventID]
	if !ok {
		t.Fatalf("no Event/Subscribe transaction carrying the event id %d; emitted Event/Subscribe entity ids %v, User/Subscribe %d",
			eventID, keysOf(byEntity), len(decodeMigrationTxs(t, w.blockTxs, "User", "Subscribe")))
	}
	if _, ok := byEntity[deletedEventID]; !ok {
		t.Errorf("no Event/Subscribe transaction for the soft-deleted subscription to event %d", deletedEventID)
	}
	if others := decodeMigrationTxs(t, w.blockTxs, "User", "Subscribe"); len(others) != 0 {
		t.Errorf("emitted %d User/Subscribe transactions, want 0: an event subscription must not be flattened to entity type User", len(others))
	}
	if len(subs) != 2 {
		t.Fatalf("emitted %d Event/Subscribe transactions, want 2 (the user subscription must not produce one)", len(subs))
	}
	if sub.GetUserId() != subscriberID {
		t.Errorf("user id = %d, want the subscriber %d", sub.GetUserId(), subscriberID)
	}
	if sub.GetSigner() != subscriberWallet {
		t.Errorf("signer = %s, want the subscriber's wallet %s", sub.GetSigner(), subscriberWallet)
	}

	// ---- replay them through the indexer -------------------------------------
	d := em.NewDispatcher(logger)
	d.Register(em.Subscribe())
	em.RegisterMigrationOverrides(d)

	for _, s := range subs {
		params := em.NewParams(&corev1.ManageEntityLegacy{
			UserId:     s.GetUserId(),
			EntityType: s.GetEntityType(),
			EntityId:   s.GetEntityId(),
			Action:     s.GetAction(),
			Metadata:   s.GetMetadata(),
			Signature:  s.GetSignature(),
			Signer:     s.GetSigner(),
			Nonce:      s.GetNonce(),
		}, 1, subscribedAt, "event-sub-test-block", "event-sub-txhash", dst, logger)
		if err := d.Dispatch(ctx, params); err != nil {
			t.Fatalf("indexing the emitted subscription to %d: %v", s.GetEntityId(), err)
		}
	}

	var entityType string
	var entityID *int64
	var isDelete bool
	var createdAt time.Time
	if err := dst.QueryRow(ctx, `
		SELECT entity_type, entity_id, is_delete, created_at
		FROM subscriptions
		WHERE subscriber_id = $1 AND user_id = $2 AND is_current = true`,
		subscriberID, eventID).Scan(&entityType, &entityID, &isDelete, &createdAt); err != nil {
		t.Fatalf("query indexed subscription: %v", err)
	}
	if entityType != "Event" {
		t.Errorf("indexed entity_type = %q, want %q", entityType, "Event")
	}
	if entityID == nil || *entityID != eventID {
		t.Errorf("indexed entity_id = %v, want the event %d", entityID, eventID)
	}
	if isDelete {
		t.Error("indexed is_delete = true, want false")
	}
	if !createdAt.Equal(subscribedAt) {
		t.Errorf("indexed created_at = %s, want the source's %s", createdAt.UTC(), subscribedAt)
	}

	if err := dst.QueryRow(ctx, `
		SELECT entity_type, entity_id, is_delete
		FROM subscriptions
		WHERE subscriber_id = $1 AND user_id = $2 AND is_current = true`,
		subscriberID, deletedEventID).Scan(&entityType, &entityID, &isDelete); err != nil {
		t.Fatalf("query indexed soft-deleted subscription: %v", err)
	}
	if entityType != "Event" || entityID == nil || *entityID != deletedEventID {
		t.Errorf("soft-deleted subscription indexed as (%q, %v), want (Event, %d)", entityType, entityID, deletedEventID)
	}
	if !isDelete {
		t.Error("soft-deleted subscription indexed with is_delete = false, want true")
	}
}

func keysOf(m map[int64]*corev1.ManageEntityLegacyMigration) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
