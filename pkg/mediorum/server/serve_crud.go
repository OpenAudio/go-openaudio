package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
)

var crudStatusTables = []string{
	"ops",
	"uploads",
	"storage_and_db_sizes",
	"qm_audio_analyses",
	"audio_previews",
	"cursors",
	// Not a crudr model -- this list only drives a pg_class relname lookup for
	// row counts, so a local-only table is fine here.
	"waveforms",
}

// audioAnalysisStatusRetryBackoff is the cutoff this endpoint uses to decide
// whether a previously-failed audio upload still counts toward the live
// backlog or has cooled into the retry-eligible window. It mirrors the
// contract of the bounded audio-analysis selector; defined locally because
// the status endpoint is a read-only observer that should not couple to the
// analyzer's scheduling internals.
const audioAnalysisStatusRetryBackoff = 24 * time.Hour

type audioAnalysisBacklogRow struct {
	Depth               int64 `gorm:"column:depth"`
	RetryExhaustedCount int64 `gorm:"column:retry_exhausted_count"`
}

type audioAnalysisBacklogStatus struct {
	Depth               int64 `json:"depth"`
	RetryExhaustedCount int64 `json:"retry_exhausted_count"`
	MaxTries            int   `json:"max_tries"`
	RetryBackoffSeconds int64 `json:"retry_backoff_seconds"`
}

type waveformStatus struct {
	Enabled         bool `json:"enabled"`
	BackfillEnabled bool `json:"backfill_enabled"`
	ArchiveEnabled  bool `json:"archive_enabled"`
	// Version is a fingerprint of the algorithm version and every parameter
	// that changes the output, so it is opaque on its own. The fields below
	// report what produced it, which is what makes it debuggable.
	Version          int `json:"version"`
	AlgorithmVersion int `json:"algorithm_version"`
	Buckets          int `json:"buckets"`
	SampleRate       int `json:"sample_rate"`
	// ByUploadState buckets each analyzable upload once, so these sum to the
	// analyzable catalog. Counting per upload rather than per blob is what
	// lets them be compared: an upload yields a 320 and sometimes a preview,
	// so a blob count and an upload count were never the same unit.
	ByUploadState map[string]int64 `json:"by_upload_state"`
	// OrphanRows counts waveform rows that resolved no upload, excluding
	// legacy content which has none. Every other figure is upload-keyed and
	// blind to them, so this is what says the rest is incomplete.
	OrphanRows int64 `json:"orphan_rows"`
	// SampledAgeNs is the age of the pass the counts came from. One pass for
	// all of them is what lets them reconcile, at the cost of describing a
	// moment slightly past.
	SampledAgeNs int64 `json:"sampled_age_ns"`
	// CursorCreatedAt is how far back through history the newest-first
	// backfill walk has reached. Empty until the first batch.
	CursorCreatedAt string `json:"cursor_created_at,omitempty"`
	CursorExhausted bool   `json:"cursor_exhausted"`
	// CursorVersion is the version the current walk is running under. A
	// mismatch with Version means a re-walk has been triggered but not yet
	// picked up.
	CursorVersion int `json:"cursor_version"`
}

// queryWaveformStatus reports backfill progress from the sampled rollup.
//
// It reads the same snapshot the console does rather than issuing its own
// counts. The rollup joins uploads to waveforms, which is affordable on a
// sweep and not on a poll, and sharing one sample is also what keeps this
// endpoint and the console from disagreeing with each other.
func (ss *MediorumServer) queryWaveformStatus(ctx context.Context) (waveformStatus, error) {
	status := waveformStatus{
		Enabled:          ss.Config.WaveformEnabled,
		BackfillEnabled:  ss.Config.WaveformBackfillEnabled,
		ArchiveEnabled:   ss.Config.WaveformArchiveEnabled,
		Version:          waveformVersion,
		AlgorithmVersion: waveformAlgorithmVersion,
		Buckets:          waveformBuckets,
		SampleRate:       waveformSampleRate,
		ByUploadState:    map[string]int64{},
	}

	ss.waveformRollupMu.Lock()
	for state, count := range ss.waveformRollup.byState {
		status.ByUploadState[state] = count
	}
	status.OrphanRows = ss.waveformRollup.orphanRows
	if !ss.waveformRollupAt.IsZero() {
		status.SampledAgeNs = int64(time.Since(ss.waveformRollupAt))
	}
	ss.waveformRollupMu.Unlock()

	var createdAt *time.Time
	var cursorVersion *int
	var exhausted bool
	err := ss.pgPool.QueryRow(ctx,
		`select created_at, exhausted, version from waveform_cursor where id = 1`,
	).Scan(&createdAt, &exhausted, &cursorVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return status, err
	}
	if createdAt != nil {
		status.CursorCreatedAt = createdAt.UTC().Format(time.RFC3339)
	}
	if cursorVersion != nil {
		status.CursorVersion = *cursorVersion
	}
	status.CursorExhausted = exhausted

	return status, nil
}

func (ss *MediorumServer) serveCrudStatus(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 5*time.Second)
	defer cancel()

	status, err := ss.crud.Status(ctx, crudStatusTables)
	if err != nil {
		return c.String(http.StatusInternalServerError, fmt.Sprintf("Failed to query crud status: %v", err))
	}

	backlog, err := ss.queryAudioAnalysisBacklogStatus(ctx, time.Now().UTC())
	if err != nil {
		return c.String(http.StatusInternalServerError, fmt.Sprintf("Failed to query audio analysis backlog: %v", err))
	}

	waveforms, err := ss.queryWaveformStatus(ctx)
	if err != nil {
		return c.String(http.StatusInternalServerError, fmt.Sprintf("Failed to query waveform status: %v", err))
	}

	c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
	return c.JSON(http.StatusOK, struct {
		*crudr.Status
		AudioAnalysisBacklog audioAnalysisBacklogStatus `json:"audio_analysis_backlog"`
		Waveforms            waveformStatus             `json:"waveforms"`
	}{
		Status:               status,
		AudioAnalysisBacklog: backlog,
		Waveforms:            waveforms,
	})
}

// queryAudioAnalysisBacklogStatus returns the live audio-analysis queue depth
// (rows still eligible to run) and the dead-letter count (rows at or above the
// retry cap). A single aggregate scan keeps the cost bounded; the predicate
// matches the bounded backlog selector exactly so the two numbers describe
// the same population the analyzer reasons about. The query is correct on a
// stock schema; when the matching partial index is present it is also cheap.
func (ss *MediorumServer) queryAudioAnalysisBacklogStatus(ctx context.Context, now time.Time) (audioAnalysisBacklogStatus, error) {
	cutoff := now.Add(-audioAnalysisStatusRetryBackoff)
	var row audioAnalysisBacklogRow
	err := ss.crud.DB.WithContext(ctx).Raw(`
		SELECT
			count(*) FILTER (
				WHERE COALESCE(audio_analysis_error_count, 0) < ?
				  AND (audio_analyzed_at IS NULL OR audio_analyzed_at <= ?)
			)::bigint AS depth,
			count(*) FILTER (
				WHERE COALESCE(audio_analysis_error_count, 0) >= ?
			)::bigint AS retry_exhausted_count
		FROM uploads
		WHERE template = 'audio'
		  AND audio_analysis_status IS DISTINCT FROM 'done'
	`, MAX_TRIES, cutoff, MAX_TRIES).Scan(&row).Error
	if err != nil {
		return audioAnalysisBacklogStatus{}, err
	}
	return audioAnalysisBacklogStatus{
		Depth:               row.Depth,
		RetryExhaustedCount: row.RetryExhaustedCount,
		MaxTries:            MAX_TRIES,
		RetryBackoffSeconds: int64(audioAnalysisStatusRetryBackoff / time.Second),
	}, nil
}
