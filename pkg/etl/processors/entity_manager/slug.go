package entity_manager

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/OpenAudio/go-openaudio/etl/db"
	"github.com/jackc/pgx/v5"
)

var slugStripRegexp = regexp.MustCompile(`!|%|#|\$|&|'|\(|\)|\*|\+|’|,|/|:|;|=|\?|@|\[|\]|\x00|\^|\.|\{|\}|"|~`)

// SanitizeSlug converts a title into a URL-friendly slug.
func SanitizeSlug(title string, recordID int64, collisionID int) string {
	sanitized := title
	sanitized = slugStripRegexp.ReplaceAllString(sanitized, "")
	sanitized = strings.TrimSpace(sanitized)
	sanitized = regexp.MustCompile(`\s+`).ReplaceAllString(sanitized, "-")
	sanitized = regexp.MustCompile(`-+`).ReplaceAllString(sanitized, "-")
	sanitized = strings.ToLower(sanitized)
	if sanitized == "" {
		sanitized = strconv.FormatInt(recordID, 10)
	}
	if collisionID > 0 {
		sanitized = fmt.Sprintf("%s-%d", sanitized, collisionID)
	}
	return sanitized
}

// GenerateSlugAndCollisionID resolves slug collisions for track_routes (task_helpers.generate_slug_and_collision_id).
func GenerateSlugAndCollisionID(ctx context.Context, dbtx db.DBTX, ownerID, trackID int64, title string) (slug, titleSlug string, collisionID int, err error) {
	titleSlug = SanitizeSlug(title, trackID, 0)
	newSlug := titleSlug

	var maxCollisionID *int
	err = dbtx.QueryRow(ctx, `
		SELECT MAX(collision_id) FROM track_routes WHERE owner_id = $1 AND title_slug = $2
	`, ownerID, titleSlug).Scan(&maxCollisionID)
	if err != nil {
		return "", "", 0, err
	}

	var existingRoute bool
	if utf8LastRuneIsDigit(newSlug) {
		existingRoute, err = trackRouteSlugExists(ctx, dbtx, ownerID, newSlug)
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
		newSlug = SanitizeSlug(title, trackID, newCollisionID)
		existingRoute, err = trackRouteSlugExists(ctx, dbtx, ownerID, newSlug)
		if err != nil {
			return "", "", 0, err
		}
		hasCollisions = existingRoute
	}

	return newSlug, titleSlug, newCollisionID, nil
}

func trackRouteSlugExists(ctx context.Context, dbtx db.DBTX, ownerID int64, slug string) (bool, error) {
	var one int
	err := dbtx.QueryRow(ctx, "SELECT 1 FROM track_routes WHERE owner_id = $1 AND slug = $2 LIMIT 1", ownerID, slug).Scan(&one)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func utf8LastRuneIsDigit(s string) bool {
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s)
	return unicode.IsDigit(r)
}
