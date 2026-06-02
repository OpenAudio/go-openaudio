package etl

import (
	"context"
	"os"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

func setupPublisherDB(t *testing.T) *pgxpool.Pool {
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
	return pool
}

func TestScheduledReleasePublisher_PublishesPastDueTrack(t *testing.T) {
	pool := setupPublisherDB(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO blocks (blockhash, parenthash, number) VALUES ('blk-pub', '', 100)
		ON CONFLICT (blockhash) DO NOTHING
	`); err != nil {
		t.Fatalf("seed block: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (user_id, handle, handle_lc, wallet, is_current, is_verified, is_deactivated, is_available, created_at, updated_at, txhash)
		VALUES (3000900, 'pubuser', 'pubuser', '0xpub', true, false, false, true, now(), now(), '')
		ON CONFLICT DO NOTHING
	`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// One past-due scheduled track.
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (track_id, owner_id, is_current, is_delete, is_unlisted, is_scheduled_release, release_date, track_segments, created_at, updated_at, txhash, blocknumber)
		VALUES (2009000, 3000900, true, false, true, true, now() - interval '1 hour', '[]', now(), now(), 'tx-past', 100)
	`); err != nil {
		t.Fatalf("seed past track: %v", err)
	}
	// One future scheduled track (should NOT publish).
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (track_id, owner_id, is_current, is_delete, is_unlisted, is_scheduled_release, release_date, track_segments, created_at, updated_at, txhash, blocknumber)
		VALUES (2009001, 3000900, true, false, true, true, now() + interval '1 day', '[]', now(), now(), 'tx-future', 100)
	`); err != nil {
		t.Fatalf("seed future track: %v", err)
	}

	logger, _ := zap.NewDevelopment()
	p := NewScheduledReleasePublisher(pool, logger)
	p.publish(ctx)

	var pastUnlisted, futureUnlisted bool
	if err := pool.QueryRow(ctx, "SELECT is_unlisted FROM tracks WHERE track_id = 2009000").Scan(&pastUnlisted); err != nil {
		t.Fatalf("past: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT is_unlisted FROM tracks WHERE track_id = 2009001").Scan(&futureUnlisted); err != nil {
		t.Fatalf("future: %v", err)
	}
	if pastUnlisted {
		t.Error("past-due scheduled track should be published (is_unlisted=false)")
	}
	if !futureUnlisted {
		t.Error("future-scheduled track should still be unlisted")
	}
}

func TestScheduledReleasePublisher_PublishesPastDueAlbum(t *testing.T) {
	pool := setupPublisherDB(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO blocks (blockhash, parenthash, number) VALUES ('blk-alb', '', 100)
		ON CONFLICT (blockhash) DO NOTHING
	`); err != nil {
		t.Fatalf("seed block: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (user_id, handle, handle_lc, wallet, is_current, is_verified, is_deactivated, is_available, created_at, updated_at, txhash)
		VALUES (3000910, 'pubalbum', 'pubalbum', '0xpubalb', true, false, false, true, now(), now(), '')
		ON CONFLICT DO NOTHING
	`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO playlists (playlist_id, playlist_owner_id, is_album, is_private, is_scheduled_release, release_date, playlist_contents, is_current, is_delete, created_at, updated_at, txhash, blocknumber)
		VALUES (400900, 3000910, true, true, true, now() - interval '1 hour', '{}', true, false, now(), now(), 'tx-album', 100)
	`); err != nil {
		t.Fatalf("seed album: %v", err)
	}

	// Non-album (regular playlist) — should NOT publish even if past due.
	if _, err := pool.Exec(ctx, `
		INSERT INTO playlists (playlist_id, playlist_owner_id, is_album, is_private, is_scheduled_release, release_date, playlist_contents, is_current, is_delete, created_at, updated_at, txhash, blocknumber)
		VALUES (400901, 3000910, false, true, true, now() - interval '1 hour', '{}', true, false, now(), now(), 'tx-pl', 100)
	`); err != nil {
		t.Fatalf("seed playlist: %v", err)
	}

	logger, _ := zap.NewDevelopment()
	p := NewScheduledReleasePublisher(pool, logger)
	p.publish(ctx)

	var albumPrivate, playlistPrivate bool
	_ = pool.QueryRow(ctx, "SELECT is_private FROM playlists WHERE playlist_id = 400900").Scan(&albumPrivate)
	_ = pool.QueryRow(ctx, "SELECT is_private FROM playlists WHERE playlist_id = 400901").Scan(&playlistPrivate)
	if albumPrivate {
		t.Error("past-due scheduled album should be published (is_private=false)")
	}
	if !playlistPrivate {
		t.Error("non-album playlist should NOT be auto-published (only albums)")
	}
}
