package entity_manager

import (
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// releaseDateLayouts mirror the formats the legacy Python indexer's
// parse_release_date accepted, in the same order:
//   - JavaScript Date.toString(), e.g. "Thu May 01 2026 00:00:00 GMT-0700"
//   - ISO 8601 with a timezone (with or without fractional seconds)
//
// A bare Unix-seconds integer is handled separately below. Anything else
// (date-only, timezone-less, etc.) is treated as "no usable date", matching
// Python returning None — the caller then falls back to the block time.
var releaseDateLayouts = []string{
	"Mon Jan 02 2006 15:04:05 GMT-0700",
	time.RFC3339Nano,
	time.RFC3339,
}

// parseReleaseDate parses a release_date metadata string. The bool reports
// whether a usable date was found; the time is normalized to UTC to match the
// legacy indexer, which stored release_date as a UTC timestamp.
func parseReleaseDate(s string) (pgtype.Timestamp, bool) {
	if s == "" {
		return pgtype.Timestamp{}, false
	}
	for _, layout := range releaseDateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return pgtype.Timestamp{Time: t.UTC(), Valid: true}, true
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return pgtype.Timestamp{Time: time.Unix(n, 0).UTC(), Valid: true}, true
	}
	return pgtype.Timestamp{}, false
}

// releaseDateOrDefault resolves the release_date for a newly created track or
// playlist. The legacy Python indexer seeds release_date to the block time and
// only overrides it when metadata carries a parseable value, so a missing or
// unparseable release_date defaults to the creation time rather than NULL
// (a NULL release_date surfaces as "Invalid Date" in clients).
func releaseDateOrDefault(raw string, blockTime time.Time) pgtype.Timestamp {
	if rd, ok := parseReleaseDate(raw); ok {
		return rd
	}
	return pgtype.Timestamp{Time: blockTime, Valid: true}
}
