package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// drainWaveformJobs is drainWaveformWork's sibling for tests that care about
// what a job carries rather than which cid it names.
func drainWaveformJobs(ss *MediorumServer) []waveformJob {
	jobs := []waveformJob{}
	for {
		select {
		case job := <-ss.waveformWork:
			jobs = append(jobs, job)
		default:
			return jobs
		}
	}
}

func withWaveformEnabled(t *testing.T, ss *MediorumServer) {
	t.Helper()
	prev := ss.Config.WaveformEnabled
	ss.Config.WaveformEnabled = true
	t.Cleanup(func() { ss.Config.WaveformEnabled = prev })
}

// The sender reads the upload row directly; the receiver would have to wait for
// that row to clear consensus. Carrying the id on the request is what keeps the
// link from depending on which of the two arrives first.
func TestPeerPullRequestCarriesUploadID(t *testing.T) {
	var body []byte
	ss := &MediorumServer{
		Config: MediorumConfig{
			Self:                 testNetwork[0].Config.Self,
			BlobStorageStreaming: true,
		},
		logger: zap.NewNop(),
		peerHTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "/internal/blobs/pull", req.URL.Path)
			var err error
			body, err = io.ReadAll(req.Body)
			require.NoError(t, err)
			return testHTTPResponse(req, http.StatusOK), nil
		})},
	}

	require.NoError(t, ss.replicateStoredFileToHost(
		context.Background(),
		"http://pull-peer.test",
		"some-cid",
		nil,
		"unused-source-key",
		nil,
		"upload-abc",
		true,
	))

	var got internalBlobPullRequest
	require.NoError(t, json.Unmarshal(body, &got))
	require.Equal(t, "upload-abc", got.UploadID)
}

// The regression this fix exists for: the puller has the blob but not the
// uploads row, because the sender publishes that row through the chain while
// this request travelled over HTTP.
func TestPulledBlobLinksWaveformWithoutLocalUploadRow(t *testing.T) {
	source := testNetwork[0]
	target := testNetwork[1]
	withWaveformEnabled(t, target)
	drainWaveformJobs(target)

	content := fmt.Sprintf("pull link regression %d", time.Now().UnixNano())
	cid, err := cidutil.ComputeFileCID(bytes.NewReader([]byte(content)))
	require.NoError(t, err)
	putInternalBlobTestObject(t, context.Background(), source.bucket, cid, content)
	t.Cleanup(func() {
		_ = source.dropFromMyBucket(cid)
		_ = target.dropFromMyBucket(cid)
		deleteTestWaveform(t, target, cid)
		drainWaveformJobs(target)
	})

	// Deliberately no uploads row on the target: resolveWaveformUploadID
	// cannot succeed here, which is the whole point.
	require.Empty(t, target.resolveWaveformUploadID(context.Background(), cid))

	require.NoError(t, target.pullFileFromHostValidated(
		context.Background(), source.Config.Self.Host, cid, nil, "upload-from-sender", true))

	jobs := drainWaveformJobs(target)
	require.Len(t, jobs, 1)
	require.Equal(t, cid, jobs[0].cid)
	require.Equal(t, "upload-from-sender", jobs[0].uploadID,
		"the sender's id must be used rather than resolved locally")
	if jobs[0].localPath != "" {
		// The worker normally owns this file; nothing ran it here.
		_ = os.Remove(jobs[0].localPath)
	}
}

// linkOrphanWaveforms repairs rows the handoff could not attribute, without
// recomputing them -- including previews, which reach their upload through
// audio_previews rather than directly.
func TestLinkOrphanWaveformsLinks320AndPreview(t *testing.T) {
	ss := testNetwork[0]
	withWaveformEnabled(t, ss)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	prefix := fmt.Sprintf("waveform-link-%d-", now.UnixNano())
	cid320 := prefix + "cid320"
	previewCID := prefix + "preview"

	upload := Upload{
		ID:               prefix + "upload",
		Template:         JobTemplateAudio,
		CreatedAt:        now,
		TranscodeResults: map[string]string{"320": cid320},
	}
	require.NoError(t, ss.crud.DB.Create(&upload).Error)
	preview := AudioPreview{CID: previewCID, SourceCID: cid320, CreatedAt: now}
	require.NoError(t, ss.crud.DB.Create(&preview).Error)
	t.Cleanup(func() {
		ss.crud.DB.Where("id = ?", upload.ID).Delete(&Upload{})
		ss.crud.DB.Where("cid = ?", previewCID).Delete(&AudioPreview{})
		deleteTestWaveform(t, ss, cid320)
		deleteTestWaveform(t, ss, previewCID)
	})

	// Both written unlinked, as the handoff would if the row had not arrived.
	insertTestWaveform(t, ss, cid320, "")
	insertTestWaveform(t, ss, previewCID, "")
	require.Empty(t, waveformUploadIDOf(t, ss, cid320))
	require.Empty(t, waveformUploadIDOf(t, ss, previewCID))

	ss.linkOrphanWaveforms(ctx)

	require.Equal(t, upload.ID, waveformUploadIDOf(t, ss, cid320))
	require.Equal(t, upload.ID, waveformUploadIDOf(t, ss, previewCID),
		"a preview links through audio_previews, not by a direct match")

	// And discovery stops treating the upload as outstanding, which is the
	// symptom an unlinked row produces: recomputed on every re-walk forever.
	batch, err := ss.nextWaveformUploadBatch(ctx, time.Time{}, "", 500)
	require.NoError(t, err)
	require.NotContains(t, waveformUploadIDs(batch), upload.ID)
}

// Legacy Qm content has no upload at all, so a null upload_id there is the
// right answer. Retrying it every sweep would be work that never converges.
func TestLinkOrphanWaveformsLeavesLegacyContentAlone(t *testing.T) {
	ss := testNetwork[0]
	withWaveformEnabled(t, ss)
	ctx := context.Background()
	cid := fmt.Sprintf("Qmwaveformlegacy%d", time.Now().UnixNano())

	insertTestWaveform(t, ss, cid, "")
	t.Cleanup(func() { deleteTestWaveform(t, ss, cid) })

	ss.linkOrphanWaveforms(ctx)

	require.Empty(t, waveformUploadIDOf(t, ss, cid))
	var status string
	require.NoError(t, ss.pgPool.QueryRow(ctx,
		`select status from waveforms where cid = $1`, cid).Scan(&status))
	require.Equal(t, "done", status, "linking must not disturb the row itself")
}

func waveformUploadIDOf(t *testing.T, ss *MediorumServer, cid string) string {
	t.Helper()
	var uploadID *string
	err := ss.pgPool.QueryRow(context.Background(),
		`select upload_id from waveforms where cid = $1`, cid).Scan(&uploadID)
	require.NoError(t, err)
	if uploadID == nil {
		return ""
	}
	return *uploadID
}

// The resolver's preview lookup joins audio_previews on a gorm-derived column
// name. A typo there fails silently -- the error is not ErrNoRows, so it is
// swallowed and reported as "no upload" -- which is indistinguishable from
// legacy content. Exercising a real preview is the only thing that catches it.
func TestResolveWaveformUploadIDFindsPreviewSource(t *testing.T) {
	ss := testNetwork[0]
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	prefix := fmt.Sprintf("waveform-resolve-%d-", now.UnixNano())
	cid320 := prefix + "cid320"
	previewCID := prefix + "preview"

	upload := Upload{
		ID:               prefix + "upload",
		Template:         JobTemplateAudio,
		CreatedAt:        now,
		TranscodeResults: map[string]string{"320": cid320},
	}
	require.NoError(t, ss.crud.DB.Create(&upload).Error)
	require.NoError(t, ss.crud.DB.Create(&AudioPreview{
		CID: previewCID, SourceCID: cid320, CreatedAt: now,
	}).Error)
	t.Cleanup(func() {
		ss.crud.DB.Where("id = ?", upload.ID).Delete(&Upload{})
		ss.crud.DB.Where("cid = ?", previewCID).Delete(&AudioPreview{})
	})

	require.Equal(t, upload.ID, ss.resolveWaveformUploadID(ctx, cid320),
		"the 320 resolves by direct match")
	require.Equal(t, upload.ID, ss.resolveWaveformUploadID(ctx, previewCID),
		"a preview resolves through audio_previews to its source 320")

	require.Empty(t, ss.resolveWaveformUploadID(ctx, prefix+"unknown"),
		"an unknown cid is still an empty result, not an error")
}

// Originals and images travel the same replication path as the 320. Decoding
// them costs an ffmpeg subprocess each and produces a waveform for a cid
// nothing asks about -- and once such a row carries an upload_id it stops
// showing up as unlinked, so the waste goes quiet.
//
// It can also mislead: with expected = 1, a done row from the original alone
// satisfies the upload's count, reporting it analyzed while the blob clients
// actually request has no waveform.
func TestPulledOriginalIsNotAnalyzed(t *testing.T) {
	source := testNetwork[0]
	target := testNetwork[1]
	withWaveformEnabled(t, target)
	drainWaveformJobs(target)

	content := fmt.Sprintf("original file, not a target %d", time.Now().UnixNano())
	cid, err := cidutil.ComputeFileCID(bytes.NewReader([]byte(content)))
	require.NoError(t, err)
	putInternalBlobTestObject(t, context.Background(), source.bucket, cid, content)
	t.Cleanup(func() {
		_ = source.dropFromMyBucket(cid)
		_ = target.dropFromMyBucket(cid)
		deleteTestWaveform(t, target, cid)
		drainWaveformJobs(target)
	})

	// transcoded=false, and no uploads row matches it as a 320 or preview.
	require.NoError(t, target.pullFileFromHostValidated(
		context.Background(), source.Config.Self.Host, cid, nil, "", false))

	require.Empty(t, drainWaveformJobs(target),
		"a blob that is not an analysis target must not be decoded")

	// The blob still replicated; only the analysis was declined.
	reader, _, err := target.readBlob(context.Background(), cidutil.ShardCID(cid))
	require.NoError(t, err)
	defer reader.Close()
}

// A peer that predates the transcoded flag still gets its 320s analyzed: a
// successful resolve is itself proof the blob is a target, since that lookup
// matches only a 320 or a selected preview.
func TestPulledBlobWithoutFlagFallsBackToResolution(t *testing.T) {
	source := testNetwork[0]
	target := testNetwork[1]
	withWaveformEnabled(t, target)
	drainWaveformJobs(target)

	now := time.Now()
	content := fmt.Sprintf("older peer 320 %d", now.UnixNano())
	cid, err := cidutil.ComputeFileCID(bytes.NewReader([]byte(content)))
	require.NoError(t, err)
	putInternalBlobTestObject(t, context.Background(), source.bucket, cid, content)

	upload := Upload{
		ID:               fmt.Sprintf("older-peer-%d", now.UnixNano()),
		Template:         JobTemplateAudio,
		CreatedAt:        now.UTC().Truncate(time.Second),
		TranscodeResults: map[string]string{"320": cid},
	}
	require.NoError(t, target.crud.DB.Create(&upload).Error)
	t.Cleanup(func() {
		target.crud.DB.Where("id = ?", upload.ID).Delete(&Upload{})
		_ = source.dropFromMyBucket(cid)
		_ = target.dropFromMyBucket(cid)
		deleteTestWaveform(t, target, cid)
		drainWaveformJobs(target)
	})

	require.NoError(t, target.pullFileFromHostValidated(
		context.Background(), source.Config.Self.Host, cid, nil, "", false))

	jobs := drainWaveformJobs(target)
	require.Len(t, jobs, 1)
	require.Equal(t, upload.ID, jobs[0].uploadID)
	if jobs[0].localPath != "" {
		_ = os.Remove(jobs[0].localPath)
	}
}
