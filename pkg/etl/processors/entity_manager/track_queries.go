package entity_manager

import (
	"context"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

func trackExists(ctx context.Context, dbtx db.DBTX, trackID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM tracks WHERE track_id = $1 AND is_current = true)", trackID).Scan(&exists)
	return exists, err
}

func trackExistsActive(ctx context.Context, dbtx db.DBTX, trackID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM tracks WHERE track_id = $1 AND is_current = true AND is_delete = false)", trackID).Scan(&exists)
	return exists, err
}
