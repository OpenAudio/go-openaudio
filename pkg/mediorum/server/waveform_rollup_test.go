package server

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gocloud.dev/blob"
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

// A preview starting at zero on a track no longer than the preview window
// re-encodes to identical bytes, so content addressing yields one cid for both
// slots. waveforms is keyed by cid, so only one row can ever exist -- expecting
// two leaves the upload permanently short, re-enqueued on every re-walk and
// decoded again each time. Seen on a live node as a stuck Partial.
func TestRollupCountsDistinctBlobsNotSlots(t *testing.T) {
	ss := testNetwork[0]
	withWaveformEnabled(t, ss)
	ctx := context.Background()
	p := fmt.Sprintf("rollup-samecid-%d-", time.Now().UnixNano())
	cid := p + "shared"

	base := forceWaveformRollup(t, ss)

	// selected_preview resolves to the very same blob as the 320.
	upload := Upload{
		ID:               p + "u",
		Template:         JobTemplateAudio,
		CreatedAt:        time.Now().UTC().Truncate(time.Second),
		SelectedPreview:  sql.NullString{String: "320_preview|0", Valid: true},
		TranscodeResults: map[string]string{"320": cid, "320_preview|0": cid},
	}
	require.NoError(t, ss.crud.DB.Create(&upload).Error)
	t.Cleanup(func() {
		ss.crud.DB.Where("id = ?", upload.ID).Delete(&Upload{})
		deleteTestWaveform(t, ss, cid)
	})

	setWaveformRow(t, ss, cid, upload.ID, waveformStatusDone, waveformVersion)
	got := forceWaveformRollup(t, ss)

	require.Equal(t, base.byState[waveformStateAnalyzed]+1, got.byState[waveformStateAnalyzed],
		"one blob, one row, and the upload is finished")
	require.Equal(t, base.byState[waveformStatePartial], got.byState[waveformStatePartial],
		"counting the shared blob twice strands it in partial forever")

	// And discovery must stop enumerating it, or the re-walk decodes it again.
	batch, err := ss.nextWaveformUploadBatch(ctx, time.Time{}, "", 500)
	require.NoError(t, err)
	require.NotContains(t, waveformUploadIDs(batch), upload.ID)

	// One job, not two, since both slots name the same blob.
	targets := waveformTargets(upload)
	require.Len(t, targets, 1)
	require.Equal(t, cid, targets[0].cid)
}

func seedQmCid(t *testing.T, ss *MediorumServer, key string) {
	t.Helper()
	_, err := ss.pgPool.Exec(context.Background(),
		`insert into qm_cids (key) values ($1) on conflict do nothing`, key)
	require.NoError(t, err)
	t.Cleanup(func() {
		ss.pgPool.Exec(context.Background(), `delete from qm_cids where key = $1`, key)
	})
}

// Legacy content has no upload row, so the discovery walk cannot see it. That
// left every legacy track permanently without a waveform -- which matters more
// than the count suggests, since the all-time trending tracks are legacy cids.
func TestLegacyWalkEnqueuesQmCidsMissingWaveforms(t *testing.T) {
	ss := testNetwork[0]
	withWaveformEnabled(t, ss)
	ctx := context.Background()
	drainWaveformWork(ss)
	clearWaveformCursor(t, ss)
	t.Cleanup(func() { drainWaveformWork(ss); clearWaveformCursor(t, ss) })

	orig := ss.Config.WaveformBackfillEnabled
	ss.Config.WaveformBackfillEnabled = true
	t.Cleanup(func() { ss.Config.WaveformBackfillEnabled = orig })

	// Sortable so the keyset walk reaches them in a known order.
	prefix := fmt.Sprintf("Qmtest%d", time.Now().UnixNano())
	pending := prefix + "a"
	already := prefix + "b"
	seedQmCid(t, ss, pending)
	seedQmCid(t, ss, already)
	insertTestWaveform(t, ss, already, "")
	t.Cleanup(func() { deleteTestWaveform(t, ss, already) })

	ss.sweepWaveformLegacy(ctx)

	queued := drainWaveformWork(ss)
	require.Contains(t, queued, pending, "a legacy cid with no waveform is work")
	require.NotContains(t, queued, already, "one already analyzed is not")
}

// The walk must resume rather than restart, or a 6M-row table is re-read from
// the top on every sweep and the tail is never reached.
func TestLegacyWalkResumesFromItsCursor(t *testing.T) {
	ss := testNetwork[0]
	withWaveformEnabled(t, ss)
	ctx := context.Background()
	drainWaveformWork(ss)
	clearWaveformCursor(t, ss)
	t.Cleanup(func() { drainWaveformWork(ss); clearWaveformCursor(t, ss) })

	prefix := fmt.Sprintf("Qmcursor%d", time.Now().UnixNano())
	first, second := prefix+"a", prefix+"b"
	seedQmCid(t, ss, first)
	seedQmCid(t, ss, second)
	t.Cleanup(func() {
		deleteTestWaveform(t, ss, first)
		deleteTestWaveform(t, ss, second)
	})

	require.NoError(t, ss.setWaveformLegacyCursor(ctx, first))
	cur, err := ss.getWaveformCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, first, cur.QmKey, "the legacy position must survive a read")

	orig := ss.Config.WaveformBackfillEnabled
	ss.Config.WaveformBackfillEnabled = true
	t.Cleanup(func() { ss.Config.WaveformBackfillEnabled = orig })

	ss.sweepWaveformLegacy(ctx)
	queued := drainWaveformWork(ss)
	require.NotContains(t, queued, first, "the walk must not re-read what it passed")
	require.Contains(t, queued, second)
}

// A legacy cid is a whole track, so it counts as one unit exactly like an
// upload -- there is no second blob for it to be partially analyzed against.
func TestRollupCountsLegacyCidsAsSingleUnits(t *testing.T) {
	ss := testNetwork[0]
	withWaveformEnabled(t, ss)
	base := forceWaveformRollup(t, ss)

	prefix := fmt.Sprintf("Qmrollup%d", time.Now().UnixNano())
	analyzed, pendingCid := prefix+"a", prefix+"b"
	seedQmCid(t, ss, analyzed)
	seedQmCid(t, ss, pendingCid)
	insertTestWaveform(t, ss, analyzed, "")
	t.Cleanup(func() { deleteTestWaveform(t, ss, analyzed) })

	got := forceWaveformRollup(t, ss)
	require.Equal(t, base.byState[waveformStateAnalyzed]+1, got.byState[waveformStateAnalyzed],
		"an analyzed legacy cid is one analyzed unit")
	require.Equal(t, base.byState[waveformStateNeverAnalyzed]+1, got.byState[waveformStateNeverAnalyzed],
		"one with no row yet is one unit of outstanding work")
}

// Legacy blobs include images. Deciding from attributes costs one metadata call;
// deciding by decoding costs a full read and an ffmpeg process, three times over.
func TestNonAudioBlobsAreRejectedFromAttributes(t *testing.T) {
	require.NotEmpty(t, blobNotAnalyzable(&blob.Attributes{ContentType: "image/jpeg", Size: 8 * 1024 * 1024}))
	require.NotEmpty(t, blobNotAnalyzable(&blob.Attributes{ContentType: "video/mp4", Size: 8 * 1024 * 1024}))
	require.NotEmpty(t, blobNotAnalyzable(&blob.Attributes{ContentType: "audio/mpeg", Size: 1024}),
		"a kilobyte cannot be a track whatever it claims to be")

	require.Empty(t, blobNotAnalyzable(&blob.Attributes{ContentType: "audio/mpeg", Size: 8 * 1024 * 1024}))
	require.Empty(t, blobNotAnalyzable(&blob.Attributes{ContentType: "", Size: 8 * 1024 * 1024}),
		"legacy blobs may carry no content type, and a wrong rejection is permanent")
	require.Empty(t, blobNotAnalyzable(nil))
}
