package server

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUploadNeedsReplication(t *testing.T) {
	const rf = 4
	full := []string{"a", "b", "c", "d"}
	short := []string{"a", "b"}
	audio := map[string]string{"320": "baeaaaiqse320"}

	cases := []struct {
		name   string
		upload *Upload
		want   bool
		why    string
	}{
		{
			name:   "both full",
			upload: &Upload{Mirrors: full, TranscodedMirrors: full, TranscodeResults: audio},
			want:   false,
		},
		{
			name:   "original short",
			upload: &Upload{Mirrors: short, TranscodedMirrors: full, TranscodeResults: audio},
			want:   true,
		},
		{
			name:   "transcode short",
			upload: &Upload{Mirrors: full, TranscodedMirrors: short, TranscodeResults: audio},
			want:   true,
			why: "the case this function exists for: the original is fully mirrored, " +
				"so nothing else in the sweep would ever look at this upload again",
		},
		{
			name: "image upload with no 320",
			upload: &Upload{
				Mirrors:          full,
				TranscodeResults: map[string]string{"original.jpg": "baeaaaiqseimg"},
			},
			want: false,
			why: "images never populate transcoded_mirrors, so an unguarded check " +
				"would match every image forever",
		},
		{
			name:   "no transcode results at all",
			upload: &Upload{Mirrors: full},
			want:   false,
		},
		{
			name:   "320 present but empty",
			upload: &Upload{Mirrors: full, TranscodeResults: map[string]string{"320": ""}},
			want:   false,
			why:    "an empty cid is not a blob; replicateTranscode would refuse it too",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, uploadNeedsReplication(tc.upload, rf), tc.why)
		})
	}
}

// insertFinderUpload writes a row with exact column text. Raw SQL on purpose:
// the legacy 'null' case below cannot be produced through gorm's json
// serializer, and it is the shape that would take the whole scan down.
func insertFinderUpload(t *testing.T, ss *MediorumServer, host, id, status, mirrors, transcodedMirrors, transcodeResults string) {
	t.Helper()
	insertFinderUploadPlaced(t, ss, host, id, status, mirrors, transcodedMirrors, transcodeResults, "[]")
}

// insertFinderUploadPlaced is the same, with an explicit placement list.
func insertFinderUploadPlaced(t *testing.T, ss *MediorumServer, host, id, status, mirrors, transcodedMirrors, transcodeResults, placementHosts string) {
	t.Helper()
	err := ss.crud.DB.Exec(`
		insert into uploads
			(id, template, orig_file_cid, status, mirrors, transcoded_mirrors,
			 transcode_results, placement_hosts, created_by, created_at, updated_at)
		values (?, 'audio', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, "baeaaaiqse"+id, status, mirrors, transcodedMirrors, transcodeResults, placementHosts,
		host, time.Now().UTC(), time.Now().UTC(),
	).Error
	require.NoError(t, err)
}

func TestUnderReplicatedUploadsSQL(t *testing.T) {
	ss := testNetwork[0]
	const rf = 4
	// A host of its own, so the shared test database's existing rows cannot
	// change the answer and this test cannot change theirs.
	host := fmt.Sprintf("http://finder-sql-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ss.crud.DB.Exec(`delete from uploads where created_by = ?`, host)
	})

	full := `["h1","h2","h3","h4"]`
	short := `["h1","h2"]`
	audio := `{"320":"baeaaaiqse320"}`

	insertFinderUpload(t, ss, host, "orig-short", JobStatusDone, short, full, audio)
	insertFinderUpload(t, ss, host, "transcode-short", JobStatusDone, full, short, audio)
	insertFinderUpload(t, ss, host, "fully-replicated", JobStatusDone, full, full, audio)
	insertFinderUpload(t, ss, host, "busy", JobStatusBusy, short, short, audio)
	insertFinderUpload(t, ss, host, "image", JobStatusDone, full, "", `{"original.jpg":"baeaaaiqseimg"}`)
	insertFinderUpload(t, ss, host, "empty-320", JobStatusDone, full, `[]`, `{"320":""}`)
	// The row that makes the guard load-bearing: jsonb_array_length raises on
	// a JSON scalar, and COALESCE does not catch one.
	insertFinderUpload(t, ss, host, "legacy-null-text", JobStatusDone, full, "null", audio)

	// image is written with SQL NULL rather than '', matching how gorm stores a
	// nil slice; done separately because the helper's parameter is a string.
	require.NoError(t, ss.crud.DB.Exec(
		`update uploads set transcoded_mirrors = null where created_by = ? and id = 'image'`, host).Error)

	selected := func(t *testing.T, where string) []string {
		t.Helper()
		var uploads []*Upload
		err := ss.crud.DB.Where(where, host, JobStatusBusy, rf, rf).Find(&uploads).Error
		require.NoError(t, err, "the scan must not raise; a raised error here is silent in production")
		ids := []string{}
		for _, u := range uploads {
			ids = append(ids, u.ID)
		}
		return ids
	}

	got := selected(t, underReplicatedUploadsSQL)
	assert.ElementsMatch(t, []string{"orig-short", "transcode-short", "legacy-null-text"}, got,
		"expected exactly the rows that still owe a copy")

	// Pin the regression itself rather than trusting the list above to have
	// caught it: this is the predicate before the fix, and the whole point is
	// that transcode-short is invisible to it.
	const predicateBeforeFix = `created_by = ? AND orig_file_cid IS NOT NULL AND orig_file_cid != '' ` +
		`AND status != ? AND jsonb_array_length(COALESCE(mirrors::jsonb, '[]'::jsonb)) < ?`
	var oldRows []*Upload
	require.NoError(t, ss.crud.DB.Where(predicateBeforeFix, host, JobStatusBusy, rf).Find(&oldRows).Error)
	oldIDs := []string{}
	for _, u := range oldRows {
		oldIDs = append(oldIDs, u.ID)
	}
	assert.NotContains(t, oldIDs, "transcode-short",
		"if the old predicate already found this row the fix is pointless and this test proves nothing")
	assert.Contains(t, oldIDs, "orig-short", "the old predicate's own case must keep working")
}

// The scan hands rows straight to uploadNeedsReplication, so a row the SQL
// selects that the Go check then drops is wasted queue depth -- and the queue
// is 100 deep for the whole node.
func TestUnderReplicatedUploadsSQLAgreesWithGoCheck(t *testing.T) {
	ss := testNetwork[0]
	const rf = 4
	host := fmt.Sprintf("http://finder-agree-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ss.crud.DB.Exec(`delete from uploads where created_by = ?`, host)
	})

	full := `["h1","h2","h3","h4"]`
	short := `["h1","h2"]`
	insertFinderUpload(t, ss, host, "orig-short", JobStatusDone, short, full, `{"320":"c320"}`)
	insertFinderUpload(t, ss, host, "transcode-short", JobStatusDone, full, short, `{"320":"c320"}`)
	insertFinderUpload(t, ss, host, "image", JobStatusDone, full, "", `{"original.jpg":"cimg"}`)
	insertFinderUpload(t, ss, host, "fully-replicated", JobStatusDone, full, full, `{"320":"c320"}`)

	var uploads []*Upload
	require.NoError(t, ss.crud.DB.Where(underReplicatedUploadsSQL, host, JobStatusBusy, rf, rf).Find(&uploads).Error)
	require.NotEmpty(t, uploads)
	for _, u := range uploads {
		assert.True(t, uploadNeedsReplication(u, rf),
			"row %s passed the SQL but the Go check drops it", u.ID)
	}
}

// An upload placed on two hosts is fully replicated at two. The worker has
// always read it that way; before this fix the query and the Go check read
// ReplicationFactor instead, so a placed upload was selected on every sweep,
// queued, and dropped again by the worker -- for the life of the row.
func TestPlacedUploadIsFullyReplicatedAtItsPlacementCount(t *testing.T) {
	full := []string{"h1", "h2", "h3", "h4"}
	placed := []string{"h1", "h2"}

	t.Run("placed and satisfied is not selected", func(t *testing.T) {
		u := &Upload{
			Mirrors:           placed,
			TranscodedMirrors: placed,
			PlacementHosts:    placed,
			TranscodeResults:  map[string]string{"320": "baeaaaiqse320"},
		}
		require.False(t, uploadNeedsReplication(u, 4),
			"a placed upload holding every host it was placed on still looked under-replicated")
	})

	t.Run("placed and genuinely short is still selected", func(t *testing.T) {
		u := &Upload{
			Mirrors:           []string{"h1"},
			TranscodedMirrors: placed,
			PlacementHosts:    placed,
			TranscodeResults:  map[string]string{"320": "baeaaaiqse320"},
		}
		require.True(t, uploadNeedsReplication(u, 4))
	})

	t.Run("placed and short on the transcode only", func(t *testing.T) {
		u := &Upload{
			Mirrors:           placed,
			TranscodedMirrors: []string{"h1"},
			PlacementHosts:    placed,
			TranscodeResults:  map[string]string{"320": "baeaaaiqse320"},
		}
		require.True(t, uploadNeedsReplication(u, 4))
	})

	t.Run("unplaced still measures against the replication factor", func(t *testing.T) {
		u := &Upload{Mirrors: placed, TranscodedMirrors: full}
		require.True(t, uploadNeedsReplication(u, 4),
			"an upload with no placement must still want ReplicationFactor copies")
	})

	t.Run("placement larger than the factor is honoured too", func(t *testing.T) {
		six := []string{"h1", "h2", "h3", "h4", "h5", "h6"}
		u := &Upload{Mirrors: full, TranscodedMirrors: six, PlacementHosts: six}
		require.True(t, uploadNeedsReplication(u, 4),
			"placement is the target in both directions, not a ceiling of ReplicationFactor")
	})
}

// The SQL has to agree, or the fix only moves the waste from the queue to the
// scan: the rows would still come back from the database on every sweep.
func TestUnderReplicatedUploadsSQLHonoursPlacement(t *testing.T) {
	ss := testNetwork[0]
	const rf = 4
	host := fmt.Sprintf("http://finder-placement-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ss.crud.DB.Exec(`delete from uploads where created_by = ?`, host)
	})

	full := `["h1","h2","h3","h4"]`
	placed := `["h1","h2"]`
	audio := `{"320":"baeaaaiqse320"}`

	insertFinderUploadPlaced(t, ss, host, "placed-satisfied", JobStatusDone, placed, placed, audio, placed)
	insertFinderUploadPlaced(t, ss, host, "placed-short", JobStatusDone, `["h1"]`, placed, audio, placed)
	insertFinderUploadPlaced(t, ss, host, "unplaced-short", JobStatusDone, placed, full, audio, `[]`)
	// A legacy row whose placement column holds the text 'null' rather than an
	// empty array: the same shape that would raise for the whole scan.
	insertFinderUploadPlaced(t, ss, host, "placement-null-text", JobStatusDone, placed, full, audio, "null")

	var uploads []*Upload
	require.NoError(t, ss.crud.DB.Where(underReplicatedUploadsSQL, host, JobStatusBusy, rf, rf).Find(&uploads).Error,
		"the scan must not raise; a raised error here is silent in production")

	ids := []string{}
	for _, u := range uploads {
		ids = append(ids, u.ID)
	}
	assert.ElementsMatch(t, []string{"placed-short", "unplaced-short", "placement-null-text"}, ids,
		"placed-satisfied is the regression: it owes nothing and must not be selected")

	// And the Go check must reach the same verdict on whatever the SQL returned,
	// or the scan is handing the worker rows it will drop.
	for _, u := range uploads {
		assert.True(t, uploadNeedsReplication(u, rf),
			"SQL selected %s but the Go check disagrees", u.ID)
	}
}
