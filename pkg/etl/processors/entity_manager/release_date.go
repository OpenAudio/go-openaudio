package entity_manager

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// releaseDateLayouts are the date formats clients have historically sent in
// entity-manager metadata. They are tried in order. The earlier strict
// RFC3339-only parsing dropped any value that did not carry a timezone (or was
// date-only), which left release_date NULL and surfaced as "Invalid Date" in
// clients. Keep the timezone-bearing layouts first so an explicit offset wins.
var releaseDateLayouts = []string{
	time.RFC3339Nano,        // 2026-05-01T00:00:00.000Z / with offset
	time.RFC3339,            // 2026-05-01T00:00:00Z / with offset
	"2006-01-02T15:04:05",   // ISO-ish, no timezone
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05",   // space separated, no timezone
	"2006-01-02",            // date only
}

// parseReleaseDate parses a release_date metadata string using the set of
// formats clients are known to emit. The bool reports whether parsing
// succeeded; on failure the caller should leave the column NULL.
func parseReleaseDate(s string) (pgtype.Timestamp, bool) {
	if s == "" {
		return pgtype.Timestamp{}, false
	}
	for _, layout := range releaseDateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return pgtype.Timestamp{Time: t, Valid: true}, true
		}
	}
	return pgtype.Timestamp{}, false
}
