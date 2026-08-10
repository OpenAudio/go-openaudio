package main

import (
	"strings"
	"testing"
)

func TestClassifyAggIdentical(t *testing.T) {
	if s, _ := classifyAgg(0, 0, 0); s != aggOK {
		t.Fatalf("0/0 should pass, got %v", s)
	}
	if s, _ := classifyAgg(943784, 943784, 0); s != aggOK {
		t.Fatalf("identical counts should pass, got %v", s)
	}
}

// The check this whole tool was extended for: tracks.playlists_containing_track
// was populated on 943,784 reference rows and none of the indexed ones, while
// the row counts differed by 19 out of ~1.96M and looked fine.
func TestClassifyAggEmptyOnIndexedSideAlwaysFails(t *testing.T) {
	for _, tol := range []float64{0, 0.5, 50, 100, 1e9} {
		s, note := classifyAgg(943784, 0, tol)
		if s != aggFail {
			t.Fatalf("tolerance %v: populated reference vs empty indexed must fail, got %v", tol, s)
		}
		if !strings.Contains(note, "empty on the indexed side") {
			t.Fatalf("tolerance %v: note should name the failure, got %q", tol, note)
		}
	}
	// Even a single row is enough: all-or-nothing means the column was not
	// indexed, not that it drifted.
	if s, _ := classifyAgg(1, 0, 100); s != aggFail {
		t.Fatalf("1 vs 0 must fail, got %v", s)
	}
}

func TestClassifyAggEmptyOnReferenceSideAlsoFails(t *testing.T) {
	s, note := classifyAgg(0, 4210, 100)
	if s != aggFail {
		t.Fatalf("rows the reference does not have must fail, got %v", s)
	}
	if !strings.Contains(note, "absent from the reference side") {
		t.Fatalf("note should name the failure, got %q", note)
	}
}

func TestClassifyAggTolerance(t *testing.T) {
	// The row-count delta that made the real bug look benign: within
	// tolerance, but still surfaced rather than reported as a pass.
	s, note := classifyAgg(1_955_896, 1_955_877, 0.5)
	if s != aggWarn {
		t.Fatalf("a 0.001%% delta should warn, got %v", s)
	}
	if !strings.Contains(note, "%") {
		t.Fatalf("warning should quantify the difference, got %q", note)
	}

	if s, _ := classifyAgg(1_955_896, 1_955_877, 0); s != aggFail {
		t.Fatalf("zero tolerance should fail any difference, got %v", s)
	}
	if s, _ := classifyAgg(1000, 900, 0.5); s != aggFail {
		t.Fatalf("a 10%% delta should fail at 0.5%% tolerance, got %v", s)
	}
	if s, _ := classifyAgg(1000, 1100, 0.5); s != aggFail {
		t.Fatalf("an overshoot should fail too, got %v", s)
	}
	if s, _ := classifyAgg(1000, 1004, 0.5); s != aggWarn {
		t.Fatalf("a 0.4%% overshoot should warn, got %v", s)
	}
}

func TestAggStatusStrings(t *testing.T) {
	for status, want := range map[aggStatus]string{aggOK: "OK", aggWarn: "WARN", aggFail: "FAIL"} {
		if got := status.String(); got != want {
			t.Fatalf("status %d = %q, want %q", status, got, want)
		}
	}
}

// jsonb columns are the trap this file exists to avoid: a column holding the
// JSON literal `null` is SQL NOT NULL, so `col IS NOT NULL` counts rows that
// carry nothing. On this dataset that mistake once inflated a count ~400x.
func TestJSONBPopulatedRejectsJSONNullAndEmpties(t *testing.T) {
	expr := jsonbPopulated("event_data")
	for _, want := range []string{
		"event_data IS NULL",
		"jsonb_typeof(event_data) = 'null'",
		"event_data = '{}'::jsonb",
		"event_data = '[]'::jsonb",
	} {
		if !strings.Contains(expr, want) {
			t.Fatalf("jsonbPopulated is missing the %q guard: %s", want, expr)
		}
	}
	if strings.Contains(expr, "IS NOT NULL") {
		t.Fatalf("jsonbPopulated must not lean on IS NOT NULL, which counts JSON null: %s", expr)
	}
	// CASE, not AND: Postgres does not promise left-to-right evaluation, so a
	// chained AND could call jsonb_array_length on a non-array.
	if !strings.HasPrefix(expr, "CASE WHEN") {
		t.Fatalf("jsonbPopulated must order its guards with CASE: %s", expr)
	}
}

func TestNoAggregateUsesRawIsNotNullOnJSONB(t *testing.T) {
	// jsonb columns in the covered schemas. If a future check reaches for
	// `col IS NOT NULL` on one of these, it is counting JSON nulls.
	jsonbCols := []string{
		"playlist_library", "playlists_previously_containing_track", "stem_of",
		"remix_of", "stream_conditions", "download_conditions", "event_data",
		"playlist_contents",
	}
	for _, ct := range compareTables {
		for _, a := range ct.Aggregates {
			for _, col := range jsonbCols {
				if strings.Contains(a.Expr, col+" IS NOT NULL") {
					t.Errorf("%s.%s uses %s IS NOT NULL, which counts the JSON literal null", ct.Name, a.Name, col)
				}
			}
		}
	}
}

func TestAggregateQueryStartsWithRowCount(t *testing.T) {
	ct := compareTable{Name: "tracks", Aggregates: []aggCheck{{"in_playlists", "count(*) FILTER (WHERE true)"}}}
	q := aggregateQuery(ct)
	if !strings.HasPrefix(q, "SELECT count(*)::bigint, ") {
		t.Fatalf("row count must lead the aggregate query: %s", q)
	}
	if !strings.HasSuffix(q, " FROM tracks") {
		t.Fatalf("query should read the table it describes: %s", q)
	}
	names := aggregateNames(ct)
	if len(names) != len(ct.Aggregates)+1 {
		t.Fatalf("labels must line up with the selected columns: %v", names)
	}
	if names[0] != "row_count" || names[1] != "in_playlists" {
		t.Fatalf("unexpected labels: %v", names)
	}
}

func TestTracksCoversTheColumnsThatCausedTheBug(t *testing.T) {
	var tracks *compareTable
	for i := range compareTables {
		if compareTables[i].Name == "tracks" {
			tracks = &compareTables[i]
		}
	}
	if tracks == nil {
		t.Fatal("tracks is no longer covered")
	}
	joined := ""
	for _, a := range tracks.Aggregates {
		joined += a.Expr + "\n"
	}
	if !strings.Contains(joined, "playlists_containing_track") {
		t.Error("no aggregate covers playlists_containing_track, the column album-purchase access depends on")
	}
	if !strings.Contains(joined, "playlists_previously_containing_track") {
		t.Error("no aggregate covers playlists_previously_containing_track")
	}
}

func TestEveryTableHasAggregatesAndIDs(t *testing.T) {
	seen := map[string]bool{}
	for _, ct := range compareTables {
		if seen[ct.Name] {
			t.Errorf("%s is defined twice", ct.Name)
		}
		seen[ct.Name] = true
		if len(ct.IDCols) == 0 {
			t.Errorf("%s has no id columns", ct.Name)
		}
		if len(ct.Aggregates) == 0 {
			t.Errorf("%s has no column-level checks, so only its row count is verified", ct.Name)
		}
		for _, a := range ct.Aggregates {
			if a.Name == "" || a.Expr == "" {
				t.Errorf("%s has an incomplete aggregate %+v", ct.Name, a)
			}
			if a.Name == "row_count" {
				t.Errorf("%s: row_count is reserved for the implicit count(*)", ct.Name)
			}
		}
	}
	// The tables this change was asked to add.
	for _, name := range []string{
		"playlist_tracks", "playlist_routes", "track_routes", "comment_mentions",
		"comment_reactions", "events", "email_access", "encrypted_emails", "track_downloads",
	} {
		if !seen[name] {
			t.Errorf("%s is still uncovered", name)
		}
	}
}

func TestProdWhereDefaultsToTheTablesOwnFilter(t *testing.T) {
	// Defaulting to "is_current = true" made every lookup against comments,
	// muted_users and dashboard_wallet_users fail with `column "is_current"
	// does not exist`, and those errors were swallowed.
	ct := compareTable{Name: "comments", Where: "is_delete = false"}
	if got := ct.prodWhere(); got != "is_delete = false" {
		t.Fatalf("prodWhere = %q, want the table's own filter", got)
	}
	ct = compareTable{Name: "follows", Where: "is_current = true", ProdWhere: "is_current = true"}
	if got := ct.prodWhere(); got != "is_current = true" {
		t.Fatalf("prodWhere = %q, want the explicit override", got)
	}
	ct = compareTable{Name: "events"}
	if got := ct.prodWhere(); got != "true" {
		t.Fatalf("prodWhere = %q, want an unconditional lookup", got)
	}
}

func TestSelectedTables(t *testing.T) {
	opts := defaultCompareOptions()
	if len(opts.selected()) != len(compareTables) {
		t.Fatal("an empty --tables must select everything")
	}
	opts.Only = []string{"tracks", " playlists "}
	got := opts.selected()
	if len(got) != 2 || got[0].Name != "tracks" || got[1].Name != "playlists" {
		t.Fatalf("--tables filter returned %+v", got)
	}
	opts.Only = []string{"nope"}
	if len(opts.selected()) != 0 {
		t.Fatal("an unknown table must not silently select everything")
	}
}

func TestSplitTables(t *testing.T) {
	if got := splitTables(""); got != nil {
		t.Fatalf("empty list should parse to nil, got %v", got)
	}
	got := splitTables("tracks, playlists ,,users")
	want := []string{"tracks", "playlists", "users"}
	if len(got) != len(want) {
		t.Fatalf("splitTables = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitTables = %v, want %v", got, want)
		}
	}
}
