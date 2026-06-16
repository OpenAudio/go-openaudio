package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"github.com/labstack/echo/v4"
)

var crudStatusTables = []string{
	"ops",
	"uploads",
	"storage_and_db_sizes",
	"qm_audio_analyses",
	"audio_previews",
	"cursors",
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

	c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
	return c.JSON(http.StatusOK, struct {
		*crudr.Status
		AudioAnalysisBacklog audioAnalysisBacklogStatus `json:"audio_analysis_backlog"`
	}{
		Status:               status,
		AudioAnalysisBacklog: backlog,
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
