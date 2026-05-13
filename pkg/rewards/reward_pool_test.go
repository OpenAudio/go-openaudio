package rewards

import (
	"testing"
)

func TestCanonicalAuthorities(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "empty input",
			in:   []string{},
			want: []string{},
		},
		{
			name: "nil input",
			in:   nil,
			want: []string{},
		},
		{
			name: "drops empty and whitespace-only entries",
			in:   []string{"", "0xabc", "   ", "\t", "0xdef"},
			want: []string{"0xabc", "0xdef"},
		},
		{
			name: "lowercases mixed case",
			in:   []string{"0xABC", "0xDeF"},
			want: []string{"0xabc", "0xdef"},
		},
		{
			name: "trims surrounding whitespace",
			in:   []string{"  0xabc ", "\t0xdef\n"},
			want: []string{"0xabc", "0xdef"},
		},
		{
			name: "deduplicates after canonicalization",
			in:   []string{"0xABC", "0xabc", "  0xABC ", "0xdef"},
			want: []string{"0xabc", "0xdef"},
		},
		{
			name: "sorts ascending",
			in:   []string{"0xdef", "0xabc", "0x123"},
			want: []string{"0x123", "0xabc", "0xdef"},
		},
		{
			name: "all whitespace results in empty",
			in:   []string{"  ", "\t", ""},
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanonicalAuthorities(tt.in)
			if !equalStringSlices(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
