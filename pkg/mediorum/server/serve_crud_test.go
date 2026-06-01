package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
	"github.com/stretchr/testify/require"
)

func TestBuildCrudSweepResponseCapsBytesAndReportsCursor(t *testing.T) {
	ss := testNetwork[0]
	ctx := context.Background()
	prefix := fmt.Sprintf("zz-crud-sweep-cap-%d-", time.Now().UnixNano())
	cleanup := func() {
		require.NoError(t, ss.crud.DB.Where("ulid LIKE ?", prefix+"%").Delete(&crudr.Op{}).Error)
	}
	cleanup()
	t.Cleanup(cleanup)

	ops := []crudr.Op{
		{
			ULID:   prefix + "01",
			Host:   ss.Config.Self.Host,
			Action: crudr.ActionCreate,
			Table:  "uploads",
			Data:   json.RawMessage(`[{"mirrors":["https://not-this-host.example"]}]`),
		},
		{
			ULID:   prefix + "02",
			Host:   ss.Config.Self.Host,
			Action: crudr.ActionCreate,
			Table:  "storage_and_db_sizes",
			Data:   json.RawMessage(`[{"host":"small","size":1}]`),
		},
		{
			ULID:   prefix + "03",
			Host:   ss.Config.Self.Host,
			Action: crudr.ActionCreate,
			Table:  "storage_and_db_sizes",
			Data:   json.RawMessage(fmt.Sprintf(`[{"host":"%s","size":2}]`, strings.Repeat("x", 512))),
		},
	}
	require.NoError(t, ss.crud.DB.Create(&ops).Error)

	firstPayload, err := json.Marshal(&ops[1])
	require.NoError(t, err)
	sweep, err := ss.buildCrudSweepResponse(ctx, prefix+"00", len(firstPayload)+2)
	require.NoError(t, err)

	require.True(t, sweep.limited)
	require.Equal(t, prefix+"02", sweep.lastScannedULID)
	require.Equal(t, 3, sweep.scannedRows)
	require.Equal(t, 1, sweep.responseRows)

	var returned []crudr.Op
	require.NoError(t, json.Unmarshal(sweep.body, &returned))
	require.Len(t, returned, 1)
	require.Equal(t, prefix+"02", returned[0].ULID)
}

func TestBuildCrudSweepResponseAdvancesPastFilteredUploads(t *testing.T) {
	ss := testNetwork[0]
	ctx := context.Background()
	prefix := fmt.Sprintf("zz-crud-sweep-filter-%d-", time.Now().UnixNano())
	cleanup := func() {
		require.NoError(t, ss.crud.DB.Where("ulid LIKE ?", prefix+"%").Delete(&crudr.Op{}).Error)
	}
	cleanup()
	t.Cleanup(cleanup)

	ops := []crudr.Op{
		{
			ULID:   prefix + "01",
			Host:   ss.Config.Self.Host,
			Action: crudr.ActionCreate,
			Table:  "uploads",
			Data:   json.RawMessage(`[{"mirrors":["https://not-this-host.example"]}]`),
		},
		{
			ULID:   prefix + "02",
			Host:   ss.Config.Self.Host,
			Action: crudr.ActionCreate,
			Table:  "uploads",
			Data:   json.RawMessage(`[{"mirrors":["https://also-not-this-host.example"]}]`),
		},
	}
	require.NoError(t, ss.crud.DB.Create(&ops).Error)

	sweep, err := ss.buildCrudSweepResponse(ctx, prefix+"00", 1024)
	require.NoError(t, err)

	require.False(t, sweep.limited)
	require.Equal(t, prefix+"02", sweep.lastScannedULID)
	require.Equal(t, 2, sweep.scannedRows)
	require.Equal(t, 0, sweep.responseRows)
	require.JSONEq(t, `[]`, string(sweep.body))
}

func TestServeCrudStatus(t *testing.T) {
	statusServer := testNetwork[0]
	requestingPeer := testNetwork[1]

	req, err := signature.SignedGet(
		context.Background(),
		statusServer.Config.Self.Host+"/internal/crud/status",
		requestingPeer.Config.privateKey,
		requestingPeer.Config.Self.Host,
	)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	var payload struct {
		crudr.Status
		PullLimit        int `json:"pull_limit"`
		MaxResponseBytes int `json:"max_response_bytes"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))

	require.Equal(t, PullLimit, payload.PullLimit)
	require.Equal(t, crudSweepMaxResponseBytes, payload.MaxResponseBytes)
	require.Contains(t, payload.Tables, "ops")
	require.Contains(t, payload.Tables, "uploads")
	require.Contains(t, payload.Tables, "qm_audio_analyses")
	require.GreaterOrEqual(t, payload.Cursors.Count, int64(0))
}

func TestQueryAudioAnalysisBacklogStatusPartitionsDepthAndExhausted(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	now := time.Now().UTC().Truncate(time.Second)
	prefix := fmt.Sprintf("audio-backlog-status-%d-", now.UnixNano())

	cleanup := func() {
		require.NoError(t, ss.crud.DB.Where("id LIKE ?", prefix+"%").Delete(&Upload{}).Error)
	}
	cleanup()
	t.Cleanup(cleanup)

	// Baselines captured before seeding so the test is robust to leftover
	// rows from earlier tests running against the same shared database.
	baseline, err := ss.queryAudioAnalysisBacklogStatus(ctx, now)
	require.NoError(t, err)

	fixtures := []Upload{
		{
			// Eligible: never analysed.
			ID:       prefix + "never-tried",
			Template: JobTemplateAudio,
		},
		{
			// Eligible: failed once, past the backoff window.
			ID:                      prefix + "old-error",
			Template:                JobTemplateAudio,
			AudioAnalysisStatus:     JobStatusError,
			AudioAnalysisErrorCount: 1,
			AudioAnalyzedAt:         now.Add(-48 * time.Hour),
		},
		{
			// Not depth, not exhausted: still inside backoff window.
			ID:                      prefix + "recent-error",
			Template:                JobTemplateAudio,
			AudioAnalysisStatus:     JobStatusError,
			AudioAnalysisErrorCount: 1,
			AudioAnalyzedAt:         now.Add(-time.Hour),
		},
		{
			// Exhausted: at the cap.
			ID:                      prefix + "exhausted",
			Template:                JobTemplateAudio,
			AudioAnalysisStatus:     JobStatusError,
			AudioAnalysisErrorCount: MAX_TRIES,
			AudioAnalyzedAt:         now.Add(-48 * time.Hour),
		},
		{
			// Excluded: already done.
			ID:                  prefix + "done",
			Template:            JobTemplateAudio,
			AudioAnalysisStatus: JobStatusDone,
		},
		{
			// Excluded: not an audio upload.
			ID:                      prefix + "image",
			Template:                JobTemplateImgSquare,
			AudioAnalysisStatus:     JobStatusError,
			AudioAnalysisErrorCount: 1,
			AudioAnalyzedAt:         now.Add(-48 * time.Hour),
		},
	}
	for i := range fixtures {
		require.NoError(t, ss.crud.DB.Create(&fixtures[i]).Error)
	}

	got, err := ss.queryAudioAnalysisBacklogStatus(ctx, now)
	require.NoError(t, err)

	require.Equal(t, baseline.Depth+2, got.Depth)
	require.Equal(t, baseline.RetryExhaustedCount+1, got.RetryExhaustedCount)
	require.Equal(t, MAX_TRIES, got.MaxTries)
	require.Equal(t, int64((24 * time.Hour).Seconds()), got.RetryBackoffSeconds)
}

func TestServeCrudStatusIncludesAudioAnalysisBacklog(t *testing.T) {
	statusServer := testNetwork[0]
	requestingPeer := testNetwork[1]

	req, err := signature.SignedGet(
		context.Background(),
		statusServer.Config.Self.Host+"/internal/crud/status",
		requestingPeer.Config.privateKey,
		requestingPeer.Config.Self.Host,
	)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var payload struct {
		AudioAnalysisBacklog struct {
			Depth               int64 `json:"depth"`
			RetryExhaustedCount int64 `json:"retry_exhausted_count"`
			MaxTries            int   `json:"max_tries"`
			RetryBackoffSeconds int64 `json:"retry_backoff_seconds"`
		} `json:"audio_analysis_backlog"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))

	require.Equal(t, MAX_TRIES, payload.AudioAnalysisBacklog.MaxTries)
	require.Equal(t, int64((24 * time.Hour).Seconds()), payload.AudioAnalysisBacklog.RetryBackoffSeconds)
	require.GreaterOrEqual(t, payload.AudioAnalysisBacklog.Depth, int64(0))
	require.GreaterOrEqual(t, payload.AudioAnalysisBacklog.RetryExhaustedCount, int64(0))
}

func TestServeCrudStatusRequiresPeerAuth(t *testing.T) {
	statusServer := testNetwork[0]

	resp, err := http.Get(statusServer.Config.Self.Host + "/internal/crud/status")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
