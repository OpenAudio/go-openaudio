package entity_manager

import "testing"

func TestNormalizeGenre(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Empty / whitespace.
		{"", ""},
		{"   ", ""},

		// Hip-Hop/Rap variants.
		{"hip-hop", "Hip-Hop/Rap"},
		{"hiphop", "Hip-Hop/Rap"},
		{"hip hop", "Hip-Hop/Rap"},
		{"Hip Hop", "Hip-Hop/Rap"},
		{"hip hop/rap", "Hip-Hop/Rap"},
		{"Hip-Hop/Rap", "Hip-Hop/Rap"},
		{"  HIP-HOP  ", "Hip-Hop/Rap"},
		{"rap", "Hip-Hop/Rap"},

		// R&B/Soul variants.
		{"r&b", "R&B/Soul"},
		{"rnb", "R&B/Soul"},
		{"RNB", "R&B/Soul"},
		{"r and b", "R&B/Soul"},
		{"r&b/soul", "R&B/Soul"},
		{"R&B/Soul", "R&B/Soul"},

		// Drum & Bass variants.
		{"drum & bass", "Drum & Bass"},
		{"drum and bass", "Drum & Bass"},
		{"dnb", "Drum & Bass"},
		{"DnB", "Drum & Bass"},
		{"Drum & Bass", "Drum & Bass"},

		// Electronic variants.
		{"edm", "Electronic"},
		{"EDM", "Electronic"},
		{"electronic dance music", "Electronic"},
		{"electronic", "Electronic"},

		// Lo-Fi variants (hyphenated canonical form).
		{"lo-fi", "Lo-Fi"},
		{"lofi", "Lo-Fi"},
		{"lo fi", "Lo-Fi"},
		{"Lo-Fi", "Lo-Fi"},

		// Already-canonical allowlist entries round-trip regardless of case.
		{"jazz", "Jazz"},
		{"DEEP HOUSE", "Deep House"},
		{"progressive house", "Progressive House"},

		// Unknown genres fall back to title-cased baseline.
		{"polka", "Polka"},
		{"j-pop fusion", "J-pop Fusion"},
		{"  some  weird genre ", "Some Weird Genre"},
	}

	for _, c := range cases {
		if got := NormalizeGenre(c.in); got != c.want {
			t.Errorf("NormalizeGenre(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Every value NormalizeGenre maps to (other than the title-cased fallback) must
// be a real GenreAllowlist entry — guards against typos drifting from the
// canonical set.
func TestNormalizeGenre_VariantTargetsAreCanonical(t *testing.T) {
	for variant, canonical := range genreVariants {
		if _, ok := GenreAllowlist[canonical]; !ok {
			t.Errorf("variant %q maps to %q which is not in GenreAllowlist", variant, canonical)
		}
	}
}
