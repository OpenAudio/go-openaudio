package server

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// forceWaveformRollup bypasses the freshness gate. The TTL exists to keep a
// sweep from re-running the pass; a test wants the pass.
func forceWaveformRollup(t *testing.T, ss *MediorumServer) waveformRollup {
	t.Helper()
	ss.waveformRollupMu.Lock()
	ss.waveformRollupAt = time.Time{}
	ss.waveformRollupMu.Unlock()

	ss.refreshWaveformRollup(context.Background())

	ss.waveformRollupMu.Lock()
	defer ss.waveformRollupMu.Unlock()
	require.False(t, ss.waveformRollupAt.IsZero(), "rollup did not complete")
	return ss.waveformRollup
}

type rollupUpload struct {
	id       string
	cid320   string
	preview  string // preview cid; empty for none
	previewK string // selected_preview key
}

func seedRollupUpload(t *testing.T, ss *MediorumServer, u rollupUpload) {
	t.Helper()
	results := map[string]string{"320": u.cid320}
	upload := Upload{
		ID:               u.id,
		Template:         JobTemplateAudio,
		CreatedAt:        time.Now().UTC().Truncate(time.Second),
		TranscodeResults: results,
	}
	if u.previewK != "" {
		upload.SelectedPreview = sql.NullString{String: u.previewK, Valid: true}
		if u.preview != "" {
			results[u.previewK] = u.preview
		}
	}
	require.NoError(t, ss.crud.DB.Create(&upload).Error)
	t.Cleanup(func() {
		ss.crud.DB.Where("id = ?", u.id).Delete(&Upload{})
		deleteTestWaveform(t, ss, u.cid320)
		if u.preview != "" {
			deleteTestWaveform(t, ss, u.preview)
		}
	})
}

func setWaveformRow(t *testing.T, ss *MediorumServer, cid, uploadID, status string, version int) {
	t.Helper()
	_, err := ss.pgPool.Exec(context.Background(), `
		insert into waveforms (cid, buckets, version, status, upload_id, analyzed_at)
		values ($1, $2, $3, $4, $5, now())
		on conflict (cid) do update set version = excluded.version,
			status = excluded.status, upload_id = excluded.upload_id
	`, cid, waveformBuckets, version, status, nullableUploadID(uploadID))
	require.NoError(t, err)
}

// The double count this rollup exists to remove: a stale row used to make its
// upload analyzed, awaiting recompute, and outstanding all at once.
func TestRollupCountsEachUploadExactlyOnce(t *testing.T) {
	ss := testNetwork[0]
	withWaveformEnabled(t, ss)
	p := fmt.Sprintf("rollup-once-%d-", time.Now().UnixNano())

	base := forceWaveformRollup(t, ss)

	seedRollupUpload(t, ss, rollupUpload{id: p + "u", cid320: p + "cid"})
	setWaveformRow(t, ss, p+"cid", p+"u", waveformStatusDone, waveformVersion+1)
	stale := forceWaveformRollup(t, ss)

	require.Equal(t, base.byState[waveformStateToRecompute]+1, stale.byState[waveformStateToRecompute],
		"a stale row puts its upload in exactly one bucket")
	require.Equal(t, base.byState[waveformStateAnalyzed], stale.byState[waveformStateAnalyzed],
		"and not also in analyzed")
	require.Equal(t, base.byState[waveformStateNeverAnalyzed], stale.byState[waveformStateNeverAnalyzed],
		"nor in never analyzed, which is what outstanding used to do")

	// Recomputing at the current version moves it rather than adding to it.
	setWaveformRow(t, ss, p+"cid", p+"u", waveformStatusDone, waveformVersion)
	fresh := forceWaveformRollup(t, ss)
	require.Equal(t, base.byState[waveformStateToRecompute], fresh.byState[waveformStateToRecompute])
	require.Equal(t, base.byState[waveformStateAnalyzed]+1, fresh.byState[waveformStateAnalyzed])
}

// A finished 320 beside a failed preview is a failure worth acting on, so the
// worse state wins rather than the upload being counted under both.
func TestRollupWorstStateWinsAcrossBlobs(t *testing.T) {
	ss := testNetwork[0]
	withWaveformEnabled(t, ss)
	p := fmt.Sprintf("rollup-worst-%d-", time.Now().UnixNano())

	seedRollupUpload(t, ss, rollupUpload{
		id: p + "u", cid320: p + "cid", preview: p + "prev", previewK: "preview|30",
	})
	setWaveformRow(t, ss, p+"cid", p+"u", waveformStatusDone, waveformVersion)
	setWaveformRow(t, ss, p+"prev", p+"u", waveformStatusError, waveformVersion)

	base := forceWaveformRollup(t, ss)

	// Demote the failure: the same upload must move to analyzed, not add to it.
	setWaveformRow(t, ss, p+"prev", p+"u", waveformStatusDone, waveformVersion)
	done := forceWaveformRollup(t, ss)

	require.Equal(t, base.byState[waveformStateFailed]-1, done.byState[waveformStateFailed],
		"the upload was filed under its worst blob")
	require.Equal(t, base.byState[waveformStateAnalyzed]+1, done.byState[waveformStateAnalyzed])
}

// A preview key naming a blob that was never produced must not make its upload
// expect a row that can never be written -- that is permanently partial work.
func TestRollupExpectationMatchesProducibleBlobs(t *testing.T) {
	ss := testNetwork[0]
	withWaveformEnabled(t, ss)
	p := fmt.Sprintf("rollup-expect-%d-", time.Now().UnixNano())

	base := forceWaveformRollup(t, ss)

	// selected_preview set, but no preview cid in transcode_results: the
	// preview blob does not exist, so waveformTargets will never emit it.
	seedRollupUpload(t, ss, rollupUpload{
		id: p + "u", cid320: p + "cid", previewK: "preview|30",
	})
	setWaveformRow(t, ss, p+"cid", p+"u", waveformStatusDone, waveformVersion)
	got := forceWaveformRollup(t, ss)

	require.Equal(t, base.byState[waveformStateAnalyzed]+1, got.byState[waveformStateAnalyzed],
		"one producible blob, one done row -- the upload is finished")
	require.Equal(t, base.byState[waveformStatePartial], got.byState[waveformStatePartial],
		"expecting an unproducible preview would strand it here forever")
}

// Uploads still transcoding have produced nothing to analyze, so counting them
// would make the tiles describe something other than the analyzable catalog.
func TestRollupIgnoresUploadsWithNoAnalyzableBlob(t *testing.T) {
	ss := testNetwork[0]
	withWaveformEnabled(t, ss)
	p := fmt.Sprintf("rollup-notyet-%d-", time.Now().UnixNano())

	before := forceWaveformRollup(t, ss)
	total := func(r waveformRollup) int64 {
		var n int64
		for _, v := range r.byState {
			n += v
		}
		return n
	}

	upload := Upload{
		ID: p + "u", Template: JobTemplateAudio,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	require.NoError(t, ss.crud.DB.Create(&upload).Error)
	t.Cleanup(func() { ss.crud.DB.Where("id = ?", upload.ID).Delete(&Upload{}) })

	after := forceWaveformRollup(t, ss)
	require.Equal(t, total(before), total(after),
		"an upload with no 320 is not yet analyzable work")
}

// The rollup is keyed by upload, so it cannot see rows that resolved none.
// Counting them separately is what stops the section quietly under-reporting.
func TestRollupCountsUnlinkedRowsSeparately(t *testing.T) {
	ss := testNetwork[0]
	withWaveformEnabled(t, ss)
	p := fmt.Sprintf("rollup-orphan-%d-", time.Now().UnixNano())

	before := forceWaveformRollup(t, ss)

	setWaveformRow(t, ss, p+"orphan", "", waveformStatusDone, waveformVersion)
	setWaveformRow(t, ss, "Qm"+p+"legacy", "", waveformStatusDone, waveformVersion)
	t.Cleanup(func() {
		deleteTestWaveform(t, ss, p+"orphan")
		deleteTestWaveform(t, ss, "Qm"+p+"legacy")
	})

	after := forceWaveformRollup(t, ss)
	require.Equal(t, before.orphanRows+1, after.orphanRows,
		"legacy content has no upload by definition and is not a miss")
}
