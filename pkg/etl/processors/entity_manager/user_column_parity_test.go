package entity_manager

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// users is the only entity whose create and update paths keep independently
// hand-written column lists -- tracks funnel both through
// insertTrackAndRouteWithState, playlists through a shared playlistRow. That
// asymmetry has now dropped four fields on create: allow_ai_attribution,
// spl_usdc_payout_wallet, profile_type and playlist_library each shipped to
// UPDATE and were missed on INSERT.
//
// This pins the two column sets together so the next added column fails here
// rather than silently vanishing on signup.
func TestUserCreateAndUpdateCoverSameColumns(t *testing.T) {
	// Columns the create path legitimately owns or the update path cannot set.
	createOnly := map[string]bool{
		"user_id": true, "wallet": true, "is_current": true, "is_storage_v2": true,
		"created_at": true, "blockhash": true,
		// set at signup only; changing them goes through dedicated actions
		"is_verified": true, "is_available": true,
	}
	// Columns an update owns, for reasons that hold independently of any
	// schema: artist_pick_track_id references a track the account cannot own
	// at signup (zero occurrences at creation across 292,111 never-updated
	// users), and is_deactivated is meaningless on create -- the create path's
	// own existence check reads is_deactivated = false.
	updateOnly := map[string]bool{
		"artist_pick_track_id": true,
		"is_deactivated":       true,
	}

	ins := columnsIn(t, readSource(t, "user_create.go"), `INSERT INTO users \(([^)]*)\)`)
	upd := columnsIn(t, readSource(t, "user_update.go"), `UPDATE users SET(.*?)WHERE`)

	var missing []string
	for c := range upd {
		if !ins[c] && !updateOnly[c] {
			missing = append(missing, c)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("columns the UPDATE path writes but the INSERT path drops: %v\n"+
			"a signup setting any of these loses it until the user next edits their profile.\n"+
			"add them to insertUserWithState, or list them in updateOnly with a reason.",
			missing)
	}

	var absent []string
	for c := range ins {
		if !upd[c] && !createOnly[c] {
			absent = append(absent, c)
		}
	}
	sort.Strings(absent)
	if len(absent) > 0 {
		t.Errorf("columns the INSERT path writes but the UPDATE path cannot change: %v\n"+
			"if that is intended, add them to createOnly with a reason.", absent)
	}
}

var colRe = regexp.MustCompile(`[a-z_][a-z_0-9]*`)

func columnsIn(t *testing.T, src, pattern string) map[string]bool {
	t.Helper()
	m := regexp.MustCompile(`(?s)` + pattern).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("could not locate %q -- the statement was reshaped; update this test", pattern)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(m[1], "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		// an UPDATE reads "col = $n"; an INSERT reads bare names
		for _, tok := range strings.Split(line, ",") {
			tok = strings.TrimSpace(strings.SplitN(tok, "=", 2)[0])
			if c := colRe.FindString(tok); c != "" && c == tok {
				out[c] = true
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("parsed zero columns from %q", pattern)
	}
	return out
}

func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := sourceFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

func sourceFile(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}
