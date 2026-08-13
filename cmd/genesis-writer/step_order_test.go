package main

import (
	"strings"
	"testing"
)

// The indexer validates references against the state a transaction lands on,
// so a step that emits rows pointing at entities a later step creates loses
// them outright -- silently, since row counts on the referencing table look
// plausible either way.
//
// Measured on the 2026-08-07 snapshot with comments running before events:
// all 69 Event comments were emitted by the writer and refused by the indexer
// with "event %d does not exist".
func TestStepOrderPutsReferencedEntitiesFirst(t *testing.T) {
	order := (&Writer{cfg: &WriterConfig{}}).stepNames()

	idx := func(name string) int {
		for i, n := range order {
			if n == name {
				return i
			}
		}
		t.Fatalf("step %q not found in %v", name, order)
		return -1
	}

	for _, dep := range []struct{ before, after, why string }{
		{"events", "comments", "a comment with entity_type=Event needs its event to exist"},
		{"events", "event subscriptions", "a subscription needs its target event to exist"},
		{"users", "tracks", "a track needs its owner"},
		{"tracks", "playlists", "playlist contents reference tracks"},
		{"comments", "comment reactions", "a reaction needs its comment"},
		{"comments", "comment pins", "a pin references a comment"},
	} {
		if b, a := idx(dep.before), idx(dep.after); b > a {
			t.Errorf("step %q runs after %q (positions %d, %d): %s",
				dep.before, dep.after, b, a, dep.why)
		}
	}

	if strings.Join(order, ",") == "" {
		t.Fatal("no steps registered")
	}
}
