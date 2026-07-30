package entity_manager

import (
	"context"
	"testing"
)

// Repro for prod issue: stem track 61740430 ("  Backing Vox 2.wav", hashid
// rq1YRQ) was created on-chain at core block 28480422 with
// stem_of.parent_track_id = 1595511751 (parent "Alone", hashid JBKl7R6), but
// the indexed row has stem_of NULL and no stems table row. Metadata below is
// the exact on-chain tx payload including the {"cid":"","data":{...}} wrapper.
func TestTrackCreate_ReproAndrewLuxStem(t *testing.T) {
	pool := setupTestDB(t)

	ownerID := int64(8151)
	parentID := int64(1595511751)
	stemID := int64(61740430)
	wallet := "0xAa942fb8d8E7Eb9024f403fC0FF63D378019Fdb5"

	seedUser(t, pool, ownerID, wallet, "andrewluxmusic")
	seedTrackFull(t, pool, parentID, ownerID, "Alone")

	meta := `{"cid":"","data":{"title":"  Backing Vox 2.wav","is_stream_gated":false,"is_unlisted":false,"field_visibility":{"genre":true,"mood":true,"tags":true,"share":true,"play_count":true},"is_downloadable":true,"stem_of":{"category":"OTHER","parent_track_id":1595511751},"owner_id":8151,"track_cid":"baeaaaiqsedlsqgtnjuvefdimo54dcl7pllqu3xrl7crjtdkwlp27gjjpoqksw","orig_file_cid":"baeaaaiqseayqdgyxk5neb7uxjmbjwhrfy2lwe7uyu64zdcpkhpz5i4lar67ao","orig_filename":"  Backing Vox 2.wav","audio_upload_id":"a194483f5cd05735bcb9463af6d60ee7","duration":179,"bpm":121,"musical_key":"G flat major","audio_analysis_error_count":0}}`

	params := buildParams(t, pool, EntityTypeTrack, ActionCreate, ownerID, stemID, wallet, meta)
	mustHandle(t, TrackCreate(), params)

	var stemOf []byte
	err := pool.QueryRow(context.Background(),
		`SELECT stem_of FROM tracks WHERE track_id=$1 AND is_current=true`, stemID).Scan(&stemOf)
	if err != nil {
		t.Fatalf("query stem_of: %v", err)
	}
	t.Logf("tracks.stem_of = %s", string(stemOf))
	if len(stemOf) == 0 {
		t.Errorf("tracks.stem_of is NULL — repro of prod bug")
	}

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM stems WHERE parent_track_id=$1 AND child_track_id=$2`,
		parentID, stemID).Scan(&count); err != nil {
		t.Fatalf("query stems: %v", err)
	}
	if count != 1 {
		t.Errorf("stems row missing (count=%d) — repro of prod bug", count)
	}

	// orig_filename must persist (Python parity): the /v1/tracks/{id}/stems
	// endpoint reads it, and rows without it broke that endpoint's row scan.
	var origFilename string
	if err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(orig_filename,'') FROM tracks WHERE track_id=$1 AND is_current=true`, stemID).Scan(&origFilename); err != nil {
		t.Fatalf("query orig_filename: %v", err)
	}
	if origFilename != "  Backing Vox 2.wav" {
		t.Errorf("orig_filename = %q, want %q", origFilename, "  Backing Vox 2.wav")
	}

	// An update carrying an explicit "stem_of": null (clients send the full
	// track object on edit) must NOT unlink the stem — same failure class as
	// the CID wipe fixed in #410.
	updMeta := `{"title":"  Backing Vox 2.wav","stem_of":null,"is_downloadable":true}`
	mustHandle(t, TrackUpdate(), buildParams(t, pool, EntityTypeTrack, ActionUpdate, ownerID, stemID, wallet, updMeta))

	if err := pool.QueryRow(context.Background(),
		`SELECT stem_of FROM tracks WHERE track_id=$1 AND is_current=true`, stemID).Scan(&stemOf); err != nil {
		t.Fatalf("query stem_of after update: %v", err)
	}
	if len(stemOf) == 0 {
		t.Errorf("explicit stem_of:null on update wiped tracks.stem_of")
	}

	// An update carrying a real stem_of value must (re)create the stems row —
	// the on-chain repair path for stems whose link was lost.
	if _, err := pool.Exec(context.Background(), `DELETE FROM stems WHERE child_track_id=$1`, stemID); err != nil {
		t.Fatalf("clear stems: %v", err)
	}
	repairMeta := `{"stem_of":{"category":"OTHER","parent_track_id":1595511751}}`
	mustHandle(t, TrackUpdate(), buildParams(t, pool, EntityTypeTrack, ActionUpdate, ownerID, stemID, wallet, repairMeta))
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM stems WHERE parent_track_id=$1 AND child_track_id=$2`,
		parentID, stemID).Scan(&count); err != nil {
		t.Fatalf("query stems after repair update: %v", err)
	}
	if count != 1 {
		t.Errorf("update with stem_of did not restore stems row (count=%d)", count)
	}
}
