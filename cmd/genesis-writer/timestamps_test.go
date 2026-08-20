package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Every timestamp the writer emits must be rendered in UTC.
//
// The source schema mixes column types: most timestamps are `timestamp
// without time zone`, which pgx decodes with a UTC location, but seven columns
// the writer reads are `timestamp with time zone` -- playlist_tracks
// created_at/updated_at, email_access and encrypted_emails created_at/
// updated_at, and users.last_active_at. pgx decodes those into time.Local.
//
// Formatting a Local-zone time yields an offset-bearing string such as
// 2024-02-26T18:00:34-08:00 instead of 2024-02-27T02:00:34Z. Both name the
// same instant, and the indexer normalizes on read, so this is not currently
// data corruption -- but it makes the emitted bytes depend on the machine's
// TZ. The same source row would produce different metadata, and therefore a
// different transaction hash, on a UTC host than on a developer's laptop. A
// genesis artifact has to be reproducible.
//
// This is a source-level check on purpose. Most metadata is built inside
// per-entity closures that cannot be called in isolation, so a behavioural
// test would cover whichever one path it happened to reach. The failure mode
// worth guarding is a *new* emission site added later without .UTC(), and only
// scanning the package catches that.
func TestAllEmittedTimestampsAreUTC(t *testing.T) {
	// A .Format(time.RFC3339Nano) whose receiver does not end in .UTC().
	// RFC3339 is matched too so that reintroducing the non-fractional layout
	// -- which silently rounded ~3M created_at values to the whole second --
	// is still covered by the .UTC() half of this guard.
	unnormalized := regexp.MustCompile(`(\.UTC\(\))?\.Format\(time\.RFC3339(Nano)?\)`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	var offenders []string
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			for _, m := range unnormalized.FindAllStringSubmatch(line, -1) {
				checked++
				if m[1] == "" { // no .UTC() before .Format
					offenders = append(offenders,
						filepath.Join(name)+":"+itoa(i+1)+"  "+strings.TrimSpace(line))
				}
			}
		}
	}

	if checked == 0 {
		t.Fatal("found no RFC3339 formatting sites -- this guard has stopped guarding anything")
	}
	if len(offenders) > 0 {
		t.Errorf("%d timestamp(s) emitted without .UTC(); on a non-UTC host these render "+
			"with a local offset and the artifact stops being reproducible:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// The premise above: a Local-zone time formats with an offset, and .UTC()
// removes it while naming the same instant. If the standard library ever
// stopped behaving this way the guard would be pointless.
func TestUTCNormalizationRemovesLocalOffset(t *testing.T) {
	zone := time.FixedZone("PST", -8*60*60)
	local := time.Date(2024, 2, 26, 18, 0, 34, 0, zone)

	if got := local.Format(time.RFC3339); got != "2024-02-26T18:00:34-08:00" {
		t.Fatalf("unnormalized = %s, want an offset-bearing timestamp", got)
	}
	got := local.UTC().Format(time.RFC3339)
	if got != "2024-02-27T02:00:34Z" {
		t.Errorf("normalized = %s, want 2024-02-27T02:00:34Z", got)
	}
	if !strings.HasSuffix(got, "Z") {
		t.Errorf("normalized timestamp %s is not zone-independent", got)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}
