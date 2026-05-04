package rewards

import (
	"strings"
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

func TestMigratedPoolAddressInvariants(t *testing.T) {
	t.Run("starts with mig_ prefix and 32-hex tail", func(t *testing.T) {
		addr := MigratedPoolAddress([]string{"0xabc"})
		if !strings.HasPrefix(addr, "mig_") {
			t.Fatalf("address %q does not start with mig_", addr)
		}
		if len(addr) != len("mig_")+32 {
			t.Fatalf("address %q length = %d, want %d", addr, len(addr), len("mig_")+32)
		}
	})

	t.Run("equivalent inputs produce identical addresses", func(t *testing.T) {
		variants := [][]string{
			{"0xabc", "0xdef"},
			{"0xdef", "0xabc"},
			{"0xABC", "0xDEF"},
			{"  0xabc  ", "\t0xdef\n"},
			{"0xabc", "0xABC", "0xdef"},
			{"", "0xabc", "  ", "0xdef"},
		}
		want := MigratedPoolAddress(variants[0])
		for i, v := range variants[1:] {
			if got := MigratedPoolAddress(v); got != want {
				t.Fatalf("variant %d (%v) produced %q, expected %q", i+1, v, got, want)
			}
		}
	})

	t.Run("different inputs produce different addresses", func(t *testing.T) {
		a := MigratedPoolAddress([]string{"0xabc"})
		b := MigratedPoolAddress([]string{"0xdef"})
		c := MigratedPoolAddress([]string{"0xabc", "0xdef"})
		if a == b || a == c || b == c {
			t.Fatalf("expected distinct addresses, got a=%q b=%q c=%q", a, b, c)
		}
	})
}

// TestMigratedPoolAddressKnownVectors pins the address algorithm. The
// expected values were produced by running CanonicalAuthorities +
// md5(comma-joined) by hand; they MUST stay stable across releases so
// production rows backfilled to mig_<md5> identifiers don't lose their
// pool reference on a Go-side recomputation.
func TestMigratedPoolAddressKnownVectors(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{"single", []string{"0xabc"}, "mig_2a3aeb7c7bcb4c46bdfbc333c7727b92"},
		{"two sorted", []string{"0xabc", "0xdef"}, "mig_d61e4a5b393eb41840571b0774d559b9"},
		{"two messy", []string{"  0xDEF", "0xABC", "0xabc"}, "mig_d61e4a5b393eb41840571b0774d559b9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MigratedPoolAddress(tt.in); got != tt.want {
				t.Fatalf("MigratedPoolAddress(%v) = %q, want %q", tt.in, got, tt.want)
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
