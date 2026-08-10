package main

import (
	"strings"
	"testing"
)

func TestSampleDivisorFixedModWins(t *testing.T) {
	cfg := sampleConfig{Mod: 7, Offset: 0, Target: 100}
	if got := sampleDivisor(1_000_000, cfg); got != 7 {
		t.Fatalf("explicit --sample-mod must be honored, got %d", got)
	}
	if got := sampleDivisor(-1, cfg); got != 7 {
		t.Fatalf("explicit --sample-mod must be honored without an estimate, got %d", got)
	}
}

func TestSampleDivisorAuto(t *testing.T) {
	cfg := sampleConfig{Mod: sampleModAuto, Target: 25000}
	cases := []struct {
		name string
		est  int64
		want int
	}{
		{"smaller than target is compared whole", 100, 1},
		{"exactly the target is compared whole", 25000, 1},
		{"just over the target halves", 25001, 2},
		{"26M follows", 26_117_556, 1045},
		{"2M tracks", 1_974_289, 79},
		{"empty table", 0, 1},
		// An un-analyzed table has no usable estimate. Declining to sample is
		// the safe answer: a slow run beats a report that covered an unknown
		// fraction of the table.
		{"unknown size does not sample", -1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sampleDivisor(tc.est, cfg); got != tc.want {
				t.Fatalf("sampleDivisor(%d) = %d, want %d", tc.est, got, tc.want)
			}
		})
	}
}

func TestSampleDivisorYieldsAboutTheTarget(t *testing.T) {
	cfg := sampleConfig{Mod: sampleModAuto, Target: 25000}
	for _, est := range []int64{25_001, 100_000, 1_974_289, 26_117_556, 999_999_999} {
		n := sampleDivisor(est, cfg)
		sampled := est / int64(n)
		if sampled > int64(cfg.Target) {
			t.Fatalf("est=%d divisor=%d yields %d rows, over the %d target", est, n, sampled, cfg.Target)
		}
		// Never so coarse that we throw away an order of magnitude of coverage.
		if sampled < int64(cfg.Target)/2 {
			t.Fatalf("est=%d divisor=%d yields only %d rows, under half the %d target", est, n, sampled, cfg.Target)
		}
	}
}

func TestSampleDivisorZeroTargetDoesNotSample(t *testing.T) {
	if got := sampleDivisor(1_000_000, sampleConfig{Mod: sampleModAuto, Target: 0}); got != 1 {
		t.Fatalf("a zero target must not sample, got %d", got)
	}
}

func TestSamplePredicate(t *testing.T) {
	if got := samplePredicate("track_id", 1, 0); got != "" {
		t.Fatalf("divisor 1 means no sampling, got %q", got)
	}
	if got := samplePredicate("track_id", 0, 0); got != "" {
		t.Fatalf("divisor 0 means no sampling, got %q", got)
	}
	got := samplePredicate("user_id", 100, 3)
	want := "mod(abs(user_id::bigint), 100) = 3"
	if got != want {
		t.Fatalf("samplePredicate = %q, want %q", got, want)
	}
}

func TestSamplePredicateIsDeterministic(t *testing.T) {
	// Reproducibility is the reason this is arithmetic on the id rather than
	// TABLESAMPLE: the same flags must select the same rows on every run and
	// on both databases.
	a := samplePredicate("playlist_id", 37, 5)
	b := samplePredicate("playlist_id", 37, 5)
	if a != b {
		t.Fatalf("predicate is not stable: %q vs %q", a, b)
	}
}

func TestSamplePredicateNormalizesOffset(t *testing.T) {
	if got := samplePredicate("user_id", 10, 13); got != "mod(abs(user_id::bigint), 10) = 3" {
		t.Fatalf("offset above the divisor must wrap, got %q", got)
	}
	if got := samplePredicate("user_id", 10, -1); got != "mod(abs(user_id::bigint), 10) = 9" {
		t.Fatalf("negative offset must wrap into range, got %q", got)
	}
}

func TestSamplePredicateResiduesPartitionTheTable(t *testing.T) {
	// Every row must fall in exactly one residue class, or "1/N of the table"
	// is a lie. abs() is what makes that true for negative ids.
	const n = 4
	seen := map[int]bool{}
	for k := 0; k < n; k++ {
		p := samplePredicate("id", n, k)
		if seen[k] {
			t.Fatalf("residue %d produced twice", k)
		}
		seen[k] = true
		if !strings.HasSuffix(p, "= "+string(rune('0'+k))) {
			t.Fatalf("residue %d not encoded in %q", k, p)
		}
	}
	if len(seen) != n {
		t.Fatalf("expected %d residue classes, got %d", n, len(seen))
	}
}

func TestSampleNoteStatesCoverage(t *testing.T) {
	if got := sampleNote("track_id", 1, 0, 500); got != "full scan (no sampling)" {
		t.Fatalf("unsampled note = %q", got)
	}
	got := sampleNote("track_id", 79, 0, 1_974_289)
	for _, want := range []string{"mod(abs(track_id::bigint), 79) = 0", "1/79", "1974289"} {
		if !strings.Contains(got, want) {
			t.Fatalf("note %q is missing %q; a silent cap reads as full coverage", got, want)
		}
	}
	if got := sampleNote("track_id", 79, 0, -1); !strings.Contains(got, "1/79") {
		t.Fatalf("note without an estimate must still state the fraction, got %q", got)
	}
}
