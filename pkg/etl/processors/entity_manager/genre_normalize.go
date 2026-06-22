package entity_manager

import (
	"strings"
	"unicode"
)

// genreVariants maps a collapsed, lowercased genre string to its canonical form
// from GenreAllowlist. It is seeded from the allowlist itself (so an
// already-canonical spelling round-trips) and extended with common community
// variants. This is the canonical source of truth for genre normalization
// across the Audius platform.
var genreVariants = buildGenreVariants()

// collapseGenre lowercases s and collapses internal whitespace runs to a single
// space, so "Hip  Hop" and "hip hop" map to the same key.
func collapseGenre(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func buildGenreVariants() map[string]string {
	m := make(map[string]string, len(GenreAllowlist)*2)
	// Seed canonical spellings so an already-canonical genre round-trips.
	for canonical := range GenreAllowlist {
		m[collapseGenre(canonical)] = canonical
	}
	// Common community variants → canonical allowlist form. Keys are already
	// collapsed/lowercased.
	for variant, canonical := range map[string]string{
		// Hip-Hop/Rap
		"hip-hop":     "Hip-Hop/Rap",
		"hiphop":      "Hip-Hop/Rap",
		"hip hop":     "Hip-Hop/Rap",
		"hip hop/rap": "Hip-Hop/Rap",
		"rap":         "Hip-Hop/Rap",
		// R&B/Soul
		"r&b":      "R&B/Soul",
		"rnb":      "R&B/Soul",
		"r and b":  "R&B/Soul",
		"r&b/soul": "R&B/Soul",
		"r&b soul": "R&B/Soul",
		// Drum & Bass
		"drum and bass": "Drum & Bass",
		"drum&bass":     "Drum & Bass",
		"dnb":           "Drum & Bass",
		"d&b":           "Drum & Bass",
		// Electronic
		"edm":                    "Electronic",
		"electronic dance music": "Electronic",
		// Lo-Fi
		"lofi":  "Lo-Fi",
		"lo fi": "Lo-Fi",
	} {
		m[variant] = canonical
	}
	return m
}

// NormalizeGenre maps a genre string to its canonical Audius form. Known
// community variants (e.g. "hiphop", "r and b", "dnb") are mapped to the
// matching GenreAllowlist entry; anything unrecognized is returned in a
// title-cased baseline form. Matching is case-insensitive and surrounding /
// repeated whitespace is trimmed and collapsed. Empty input returns "".
func NormalizeGenre(genre string) string {
	key := collapseGenre(genre)
	if key == "" {
		return ""
	}
	if canonical, ok := genreVariants[key]; ok {
		return canonical
	}
	return titleCaseGenre(genre)
}

// titleCaseGenre applies a title-case baseline: each whitespace-delimited word
// is lowercased then its first rune upper-cased. Used only for genres that have
// no known canonical mapping.
func titleCaseGenre(s string) string {
	fields := strings.Fields(s)
	for i, f := range fields {
		runes := []rune(strings.ToLower(f))
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
		}
		fields[i] = string(runes)
	}
	return strings.Join(fields, " ")
}
