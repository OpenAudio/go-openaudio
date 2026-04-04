package entity_manager

import (
	"context"
	"errors"

	"github.com/OpenAudio/go-openaudio/etl/db"
	"github.com/jackc/pgx/v5"
)

func playlistExists(ctx context.Context, dbtx db.DBTX, playlistID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM playlists WHERE playlist_id = $1)", playlistID).Scan(&exists)
	return exists, err
}

func playlistExistsActive(ctx context.Context, dbtx db.DBTX, playlistID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM playlists WHERE playlist_id = $1 AND is_current = true AND is_delete = false)", playlistID).Scan(&exists)
	return exists, err
}

func playlistRouteSlugExists(ctx context.Context, dbtx db.DBTX, ownerID int64, slug string) (bool, error) {
	var one int
	err := dbtx.QueryRow(ctx, "SELECT 1 FROM playlist_routes WHERE owner_id = $1 AND slug = $2 LIMIT 1", ownerID, slug).Scan(&one)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GeneratePlaylistSlugAndCollisionID resolves slug collisions for playlist_routes.
func GeneratePlaylistSlugAndCollisionID(ctx context.Context, dbtx db.DBTX, ownerID, playlistID int64, name string) (slug, titleSlug string, collisionID int, err error) {
	titleSlug = SanitizeSlug(name, playlistID, 0)
	newSlug := titleSlug

	var maxCollisionID *int
	err = dbtx.QueryRow(ctx, `
		SELECT MAX(collision_id) FROM playlist_routes WHERE owner_id = $1 AND title_slug = $2
	`, ownerID, titleSlug).Scan(&maxCollisionID)
	if err != nil {
		return "", "", 0, err
	}

	var existingRoute bool
	if utf8LastRuneIsDigit(newSlug) {
		existingRoute, err = playlistRouteSlugExists(ctx, dbtx, ownerID, newSlug)
		if err != nil {
			return "", "", 0, err
		}
	}

	newCollisionID := 0
	hasCollisions := existingRoute
	if maxCollisionID != nil {
		hasCollisions = true
		newCollisionID = *maxCollisionID
	}

	for hasCollisions {
		newCollisionID++
		newSlug = SanitizeSlug(name, playlistID, newCollisionID)
		existingRoute, err = playlistRouteSlugExists(ctx, dbtx, ownerID, newSlug)
		if err != nil {
			return "", "", 0, err
		}
		hasCollisions = existingRoute
	}

	return newSlug, titleSlug, newCollisionID, nil
}
