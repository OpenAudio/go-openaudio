package entity_manager

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// junctionRows replays existing playlist_tracks rows for one playlist.
type junctionRows struct {
	rows [][2]any // {trackID int64, isRemoved bool}
	i    int
}

func (r *junctionRows) Next() bool {
	r.i++
	return r.i <= len(r.rows)
}

func (r *junctionRows) Scan(dest ...any) error {
	row := r.rows[r.i-1]
	*(dest[0].(*int64)) = row[0].(int64)
	*(dest[1].(*bool)) = row[1].(bool)
	return nil
}

func (r *junctionRows) Close()                                       {}
func (r *junctionRows) Err() error                                   { return nil }
func (r *junctionRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *junctionRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *junctionRows) Values() ([]any, error)                       { return nil, nil }
func (r *junctionRows) RawValues() [][]byte                          { return nil }
func (r *junctionRows) Conn() *pgx.Conn                              { return nil }

// indexDBTX records every statement so a test can assert which tables were
// written and with what arguments.
type indexDBTX struct {
	existing [][2]any
	execs    []execCall
}

type execCall struct {
	sql  string
	args []any
}

func (d *indexDBTX) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	d.execs = append(d.execs, execCall{sql: sql, args: args})
	return pgconn.CommandTag{}, nil
}
func (d *indexDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return &junctionRows{rows: d.existing}, nil
}
func (d *indexDBTX) QueryRow(context.Context, string, ...any) pgx.Row { return stubRow{} }

// trackIndexWrites returns the statements that touched the tracks reverse index.
func (d *indexDBTX) trackIndexWrites() []execCall {
	var out []execCall
	for _, e := range d.execs {
		if strings.Contains(e.sql, "UPDATE tracks") {
			out = append(out, e)
		}
	}
	return out
}

func contentsWith(trackIDs ...int64) map[string]any {
	entries := make([]any, 0, len(trackIDs))
	for _, id := range trackIDs {
		entries = append(entries, map[string]any{"track": float64(id), "time": float64(1)})
	}
	return map[string]any{"playlist_contents": entries}
}

// A track added to a playlist must gain the playlist in the reverse index —
// that index is what grants an album buyer access to the track.
func TestUpdatePlaylistTracks_AddMaintainsReverseIndex(t *testing.T) {
	db := &indexDBTX{}
	blockTime := time.Unix(1725873897, 0).UTC()

	if err := updatePlaylistTracks(context.Background(), db, 99, contentsWith(1, 2), blockTime); err != nil {
		t.Fatalf("updatePlaylistTracks: %v", err)
	}

	writes := db.trackIndexWrites()
	if len(writes) != 1 {
		t.Fatalf("got %d tracks writes, want 1", len(writes))
	}
	w := writes[0]
	if !strings.Contains(w.sql, "array_append") {
		t.Errorf("add statement does not append to playlists_containing_track:\n%s", w.sql)
	}
	if got := w.args[0]; got != int64(99) {
		t.Errorf("playlist id = %v, want 99", got)
	}
	ids, ok := w.args[1].([]int64)
	if !ok || len(ids) != 2 {
		t.Fatalf("track ids = %#v, want both added tracks", w.args[1])
	}
}

// Removing a track must record when it left, in block time. The API compares
// that timestamp against a purchase date to decide whether the buyer keeps
// access, so wall-clock here would silently change who is entitled.
func TestUpdatePlaylistTracks_RemoveRecordsBlockTime(t *testing.T) {
	blockTime := time.Unix(1725873897, 0).UTC()
	db := &indexDBTX{existing: [][2]any{{int64(1), false}, {int64(2), false}}}

	// Track 2 drops out of the contents.
	if err := updatePlaylistTracks(context.Background(), db, 99, contentsWith(1), blockTime); err != nil {
		t.Fatalf("updatePlaylistTracks: %v", err)
	}

	writes := db.trackIndexWrites()
	if len(writes) != 1 {
		t.Fatalf("got %d tracks writes, want 1 (removal only)", len(writes))
	}
	w := writes[0]
	if !strings.Contains(w.sql, "array_remove") {
		t.Errorf("statement is not the removal path:\n%s", w.sql)
	}
	if got := w.args[3]; got != blockTime.Unix() {
		t.Errorf("removal time = %v, want block time %v", got, blockTime.Unix())
	}
	ids := w.args[1].([]int64)
	if len(ids) != 1 || ids[0] != 2 {
		t.Errorf("removed track ids = %v, want [2]", ids)
	}
}

// A playlist update that does not change membership must not touch tracks at
// all: every tracks row written fires handle_track, which recounts the
// owner's catalog.
func TestUpdatePlaylistTracks_NoMembershipChangeSkipsTrackWrites(t *testing.T) {
	db := &indexDBTX{existing: [][2]any{{int64(1), false}, {int64(2), false}}}

	if err := updatePlaylistTracks(context.Background(), db, 99, contentsWith(1, 2), time.Unix(1, 0)); err != nil {
		t.Fatalf("updatePlaylistTracks: %v", err)
	}

	if writes := db.trackIndexWrites(); len(writes) != 0 {
		t.Errorf("got %d tracks writes for an unchanged playlist, want 0", len(writes))
	}
}

// The removal record's shape is a wire contract with the API's access check.
// It reads a jsonb object keyed by playlist id, so an array would silently
// grant nobody access.
func TestRemovalRecordShapeMatchesAPIContract(t *testing.T) {
	db := &indexDBTX{existing: [][2]any{{int64(2), false}}}
	blockTime := time.Unix(1725873897, 0).UTC()

	if err := updatePlaylistTracks(context.Background(), db, 1284768821, contentsWith(), blockTime); err != nil {
		t.Fatalf("updatePlaylistTracks: %v", err)
	}
	writes := db.trackIndexWrites()
	if len(writes) != 1 {
		t.Fatalf("got %d tracks writes, want 1", len(writes))
	}
	// The statement builds {"<playlist_id>": {"time": <epoch>}}; assert the
	// key is the playlist id as text and the value carries "time".
	sql := writes[0].sql
	if !strings.Contains(sql, `ARRAY[$1::text]`) || !strings.Contains(sql, `jsonb_build_object('time'`) {
		t.Errorf("removal record is not {\"<playlist_id>\": {\"time\": ...}}:\n%s", sql)
	}

	// Guard against regressing to the array shape the API's Go struct expects
	// but production has never used.
	sample := `{"1284768821": {"time": 1725873897}}`
	var asObject map[string]map[string]int64
	if err := json.Unmarshal([]byte(sample), &asObject); err != nil {
		t.Fatalf("production shape must unmarshal as an object: %v", err)
	}
	if asObject["1284768821"]["time"] != blockTime.Unix() {
		t.Errorf("time = %v, want %v", asObject["1284768821"]["time"], blockTime.Unix())
	}
}

// End-to-end against a real database: the reverse index on tracks must follow
// membership through add, removal, and re-add. This is the column the API's
// usdc_purchase check reads to grant an album buyer access to its tracks; it
// sat at its default for every track indexed by this package until now.
func TestPlaylistLifecycle_MaintainsTrackReverseIndex(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 420)
	pid := int64(PlaylistIDOffset + 4200)
	t1 := int64(TrackIDOffset + 6200)
	t2 := int64(TrackIDOffset + 6201)

	seedUser(t, pool, uid, "0xplrevidx", "prx")
	seedTrackFull(t, pool, t1, uid, "Kept")
	seedTrackFull(t, pool, t2, uid, "Dropped")

	readIndex := func(trackID int64) ([]int32, string) {
		t.Helper()
		var containing []int32
		var previously string
		if err := pool.QueryRow(context.Background(),
			`SELECT playlists_containing_track, playlists_previously_containing_track::text
			 FROM tracks WHERE track_id = $1 AND is_current = true`,
			trackID).Scan(&containing, &previously); err != nil {
			t.Fatalf("read reverse index for %d: %v", trackID, err)
		}
		return containing, previously
	}
	holds := func(ids []int32, want int64) bool {
		for _, id := range ids {
			if int64(id) == want {
				return true
			}
		}
		return false
	}

	createMeta := `{"playlist_name":"Album","is_album":true,"playlist_contents":{"track_ids":[{"track":2006200,"time":1700000000},{"track":2006201,"time":1700000100}]}}`
	mustHandle(t, PlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xplrevidx", createMeta))

	for _, id := range []int64{t1, t2} {
		if containing, _ := readIndex(id); !holds(containing, pid) {
			t.Fatalf("after create, track %d containing = %v, want to include %d", id, containing, pid)
		}
	}

	// Drop t2 from the album.
	dropMeta := `{"playlist_contents":{"track_ids":[{"track":2006200,"time":1700000000}]}}`
	mustHandle(t, PlaylistUpdate(),
		buildParams(t, pool, EntityTypePlaylist, ActionUpdate, uid, pid, "0xplrevidx", dropMeta))

	if containing, _ := readIndex(t1); !holds(containing, pid) {
		t.Errorf("kept track lost the playlist: %v", containing)
	}
	containing, previously := readIndex(t2)
	if holds(containing, pid) {
		t.Errorf("dropped track still lists the playlist: %v", containing)
	}
	// The removal record is an object keyed by playlist id, carrying "time" —
	// the shape the API's access check reads.
	var record map[string]map[string]int64
	if err := json.Unmarshal([]byte(previously), &record); err != nil {
		t.Fatalf("removal record %q is not a jsonb object: %v", previously, err)
	}
	entry, ok := record[itoa(pid)]
	if !ok {
		t.Fatalf("removal record %q has no entry for playlist %d", previously, pid)
	}
	if entry["time"] == 0 {
		t.Errorf("removal record carries no time: %q", previously)
	}

	// Re-adding must clear the removal record, or a stale entry would keep
	// granting access on a purchase that predates it.
	mustHandle(t, PlaylistUpdate(),
		buildParams(t, pool, EntityTypePlaylist, ActionUpdate, uid, pid, "0xplrevidx", createMeta))

	containing, previously = readIndex(t2)
	if !holds(containing, pid) {
		t.Errorf("re-added track missing from containing index: %v", containing)
	}
	if strings.Contains(previously, itoa(pid)) {
		t.Errorf("re-added track kept a stale removal record: %q", previously)
	}
}
