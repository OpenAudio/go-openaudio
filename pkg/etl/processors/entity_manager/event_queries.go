package entity_manager

import (
	"context"
	"time"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

func eventExists(ctx context.Context, dbtx db.DBTX, eventID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM events WHERE event_id = $1 AND is_deleted = false)",
		eventID).Scan(&exists)
	return exists, err
}

func eventOwner(ctx context.Context, dbtx db.DBTX, eventID int64) (int64, error) {
	var ownerID int64
	err := dbtx.QueryRow(ctx,
		"SELECT user_id FROM events WHERE event_id = $1 AND is_deleted = false",
		eventID).Scan(&ownerID)
	return ownerID, err
}

func eventTypeAndEndDate(ctx context.Context, dbtx db.DBTX, eventID int64) (string, *time.Time, error) {
	var evtType string
	var endDate *time.Time
	err := dbtx.QueryRow(ctx,
		"SELECT event_type, end_date FROM events WHERE event_id = $1 AND is_deleted = false",
		eventID).Scan(&evtType, &endDate)
	return evtType, endDate, err
}

func trackOwner(ctx context.Context, dbtx db.DBTX, trackID int64) (int64, error) {
	var ownerID int64
	err := dbtx.QueryRow(ctx,
		"SELECT owner_id FROM tracks WHERE track_id = $1 AND is_current = true LIMIT 1",
		trackID).Scan(&ownerID)
	return ownerID, err
}
