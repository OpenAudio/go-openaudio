package entity_manager

import (
	"context"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

func commentExists(ctx context.Context, dbtx db.DBTX, commentID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM comments WHERE comment_id = $1)",
		commentID).Scan(&exists)
	return exists, err
}

func commentExistsActive(ctx context.Context, dbtx db.DBTX, commentID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM comments WHERE comment_id = $1 AND (is_delete = false OR is_delete IS NULL))",
		commentID).Scan(&exists)
	return exists, err
}

func commentReactionExists(ctx context.Context, dbtx db.DBTX, userID, commentID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM comment_reactions WHERE user_id = $1 AND comment_id = $2 AND is_delete = false)",
		userID, commentID).Scan(&exists)
	return exists, err
}

func commentReportExists(ctx context.Context, dbtx db.DBTX, userID, commentID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM comment_reports WHERE user_id = $1 AND comment_id = $2)",
		userID, commentID).Scan(&exists)
	return exists, err
}

func getCommentOwner(ctx context.Context, dbtx db.DBTX, commentID int64) (int64, error) {
	var userID int64
	err := dbtx.QueryRow(ctx,
		"SELECT user_id FROM comments WHERE comment_id = $1 AND (is_delete = false OR is_delete IS NULL)",
		commentID).Scan(&userID)
	return userID, err
}

func getCommentEntityID(ctx context.Context, dbtx db.DBTX, commentID int64) (int64, error) {
	var entityID int64
	err := dbtx.QueryRow(ctx,
		"SELECT entity_id FROM comments WHERE comment_id = $1",
		commentID).Scan(&entityID)
	return entityID, err
}

func getTrackOwner(ctx context.Context, dbtx db.DBTX, trackID int64) (int64, error) {
	var ownerID int64
	err := dbtx.QueryRow(ctx,
		"SELECT owner_id FROM tracks WHERE track_id = $1 AND is_current = true",
		trackID).Scan(&ownerID)
	return ownerID, err
}
