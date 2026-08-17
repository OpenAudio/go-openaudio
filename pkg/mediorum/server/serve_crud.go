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
	Enabled         bool             `json:"enabled"`
	BackfillEnabled bool             `json:"backfill_enabled"`
	ArchiveEnabled  bool             `json:"archive_enabled"`
	Version         int              `json:"version"`
	ByStatus        map[string]int64 `json:"by_status"`
	// StaleVersion counts completed rows produced by an older algorithm, i.e.
	// the size of the re-sweep a version bump implies.
	StaleVersion int64 `json:"stale_version"`
	// CursorCreatedAt is how far back through history the newest-first
	// backfill walk has reached. Empty until the first batch.
	CursorCreatedAt string `json:"cursor_created_at,omitempty"`
	CursorExhausted bool   `json:"cursor_exhausted"`
}

// queryWaveformStatus reports backfill progress from the waveforms table only.
//
// The tempting query is an anti-join against uploads to show "how many are
// left", but that full-scans a wide jsonb table on every poll and is at its
// most expensive precisely when the backfill is complete and the answer is
// zero. Counting the rows we did write is indexed and bounded, and the
// archive_skipped bucket is what tells an operator the size of the bill before
// they turn OPENAUDIO_WAVEFORM_ARCHIVE_ENABLED on.
func (ss *MediorumServer) queryWaveformStatus(ctx context.Context) (waveformStatus, error) {
	status := waveformStatus{
		Enabled:         ss.Config.WaveformEnabled,
		BackfillEnabled: ss.Config.WaveformBackfillEnabled,
		ArchiveEnabled:  ss.Config.WaveformArchiveEnabled,
		Version:         waveformVersion,
		ByStatus:        map[string]int64{},
	}

	rows, err := ss.pgPool.Query(ctx, `
		select status, count(*)::bigint,
		       count(*) filter (where status = $1 and version < $2)::bigint
		from waveforms group by status
	`, waveformStatusDone, waveformVersion)
	if err != nil {
		return status, err
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		var count, stale int64
		if err := rows.Scan(&name, &count, &stale); err != nil {
			return status, err
		}
		status.ByStatus[name] = count
		status.StaleVersion += stale
	}
	if err := rows.Err(); err != nil {
		return status, err
	}

	var createdAt *time.Time
	var exhausted bool
	err = ss.pgPool.QueryRow(ctx,
		`select created_at, exhausted from waveform_cursor where id = 1`,
	).Scan(&createdAt, &exhausted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return status, err
	}
	if createdAt != nil {
		status.CursorCreatedAt = createdAt.UTC().Format(time.RFC3339)
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
