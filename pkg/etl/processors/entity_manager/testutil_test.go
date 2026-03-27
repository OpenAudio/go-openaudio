package entity_manager

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/etl/db"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// setupTestDB connects to a test Postgres, runs all ETL migrations (down then up),
// and returns the pool and a cleanup function.
//
// Set ETL_TEST_DB_URL to override the default connection string.
// Example: ETL_TEST_DB_URL="postgres://localhost:5432/etl_test?sslmode=disable"
func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dbURL := os.Getenv("ETL_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("ETL_TEST_DB_URL not set, skipping database test")
	}

	logger, _ := zap.NewDevelopment()

	if err := db.RunMigrations(logger, dbURL, true); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("failed to connect to test db: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	return pool
}

// seedUser inserts a user row into the users table for stateful validation tests.
func seedUser(t *testing.T, pool *pgxpool.Pool, userID int64, wallet, handle string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO users (user_id, handle, handle_lc, wallet, is_current, is_verified, is_deactivated, is_available, created_at, updated_at, txhash)
		VALUES ($1, $2, $3, $4, true, false, false, true, now(), now(), '')
		ON CONFLICT DO NOTHING
	`, userID, handle, strings.ToLower(handle), wallet)
	if err != nil {
		t.Fatalf("seedUser(%d): %v", userID, err)
	}
}

// seedTrack inserts a track row into the tracks table for stateful validation tests.
func seedTrack(t *testing.T, pool *pgxpool.Pool, trackID int64, ownerID int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO tracks (track_id, owner_id, is_current, is_delete, track_segments, created_at, updated_at, txhash)
		VALUES ($1, $2, true, false, '[]', now(), now(), '')
		ON CONFLICT DO NOTHING
	`, trackID, ownerID)
	if err != nil {
		t.Fatalf("seedTrack(%d): %v", trackID, err)
	}
}

// seedPlaylist inserts a playlist row into the playlists table.
func seedPlaylist(t *testing.T, pool *pgxpool.Pool, playlistID int64, ownerID int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO playlists (playlist_id, playlist_owner_id, is_album, is_private, playlist_contents, is_current, is_delete, created_at, updated_at, txhash)
		VALUES ($1, $2, false, false, '{}', true, false, now(), now(), '')
		ON CONFLICT DO NOTHING
	`, playlistID, ownerID)
	if err != nil {
		t.Fatalf("seedPlaylist(%d): %v", playlistID, err)
	}
}

// buildManageEntityTx creates a SignedTransaction wrapping a ManageEntityLegacy proto.
func buildManageEntityTx(entityType, action string, userID, entityID int64, signer, metadata string) *corev1.SignedTransaction {
	return &corev1.SignedTransaction{
		Transaction: &corev1.SignedTransaction_ManageEntity{
			ManageEntity: &corev1.ManageEntityLegacy{
				UserId:     userID,
				EntityType: entityType,
				EntityId:   entityID,
				Action:     action,
				Metadata:   metadata,
				Signer:     signer,
				Nonce:      fmt.Sprintf("nonce-%d-%d", userID, entityID),
			},
		},
	}
}

// buildParams creates Params from a ManageEntityLegacy tx and a test DB pool.
func buildParams(t *testing.T, pool *pgxpool.Pool, entityType, action string, userID, entityID int64, signer, metadata string) *Params {
	t.Helper()
	tx := buildManageEntityTx(entityType, action, userID, entityID, signer, metadata)
	logger, _ := zap.NewDevelopment()
	return NewParams(
		tx.GetManageEntity(),
		100,
		time.Now(),
		fmt.Sprintf("txhash-%s-%s-%d", entityType, action, entityID),
		pool,
		logger,
	)
}

// mustHandle asserts that a handler processes params without error.
func mustHandle(t *testing.T, h Handler, params *Params) {
	t.Helper()
	if err := h.Handle(context.Background(), params); err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
}

// mustReject asserts that a handler returns a ValidationError containing wantSubstr.
func mustReject(t *testing.T, h Handler, params *Params, wantSubstr string) {
	t.Helper()
	err := h.Handle(context.Background(), params)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !IsValidationError(err) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	if wantSubstr != "" && !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("error %q does not contain %q", err.Error(), wantSubstr)
	}
}
