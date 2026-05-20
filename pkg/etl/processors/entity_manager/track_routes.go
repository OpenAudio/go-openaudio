package entity_manager

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/OpenAudio/go-openaudio/etl/db"
	"github.com/jackc/pgx/v5"
)

var routeIDStripRegexp = regexp.MustCompile(`!|%|\x60|#|\$|&|'|\(|\)|\*|\+|,|/|:|;|=|\?|@|\[|\]|\x00`)

// CreateTrackRouteID constructs a track's route_id from an unsanitized title and handle.
// Resulting route_ids are of the shape `<handle>/<sanitized_title>`.
func CreateTrackRouteID(title, handle string) string {
	sanitized := routeIDStripRegexp.ReplaceAllString(title, "")
	sanitized = regexp.MustCompile(`\s+`).ReplaceAllString(sanitized, "-")
	sanitized = regexp.MustCompile(`-+`).ReplaceAllString(sanitized, "-")
	sanitized = strings.ToLower(sanitized)
	return strings.ToLower(handle) + "/" + sanitized
}

// getTrackOwnerHandle returns the user's handle, or a synthetic guest handle
// (`user-<id>`) when no handle is set.
func getTrackOwnerHandle(ctx context.Context, dbtx db.DBTX, userID int64) (string, error) {
	var handle *string
	err := dbtx.QueryRow(ctx, "SELECT handle FROM users WHERE user_id = $1 AND is_current = true LIMIT 1", userID).Scan(&handle)
	if err != nil && err != pgx.ErrNoRows {
		return "", err
	}
	if handle != nil && *handle != "" {
		return *handle, nil
	}
	return fmt.Sprintf("user-%d", userID), nil
}
