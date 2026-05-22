package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/env"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"go.uber.org/zap"

	"github.com/labstack/echo/v4"
)

const PullLimit = 10000

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

func (ss *MediorumServer) serveCrudSweep(c echo.Context) error {
	ss.crudSweepMutex.Lock()
	defer ss.crudSweepMutex.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	after := c.QueryParam("after")
	var ops []*crudr.Op
	err := ss.crud.DB.
		WithContext(ctx).
		Where("ulid > ?", after).
		Limit(PullLimit).
		Order("ulid asc").
		Find(&ops).
		Error
	if err != nil {
		return c.String(500, fmt.Sprintf("Failed to query ops: %v", err))
	}

	// some peers can't talk to each other, so we do some gossip
	// before we'd send all ops to all peers gossip style
	// but this is a bit excessive what with the bandwidth
	// so we only forward ops for which we are an orig upload mirror
	// thus using rendezvous for gossip forwarding
	filteredOps := make([]*crudr.Op, 0, len(ops)/2)
	myHost := []byte(ss.Config.Self.Host)
	for _, op := range ops {
		// if our host doesn't appear in the record, we are not a mirror
		if op.Table == "uploads" && !bytes.Contains(op.Data, myHost) {
			continue
		}
		filteredOps = append(filteredOps, op)
	}

	c.Response().Header().Set(echo.HeaderCacheControl, "public, max-age=300")
	return c.JSON(200, filteredOps)
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
		PullLimit            int                        `json:"pull_limit"`
		AudioAnalysisBacklog audioAnalysisBacklogStatus `json:"audio_analysis_backlog"`
	}{
		Status:               status,
		PullLimit:            PullLimit,
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

func (ss *MediorumServer) serveCrudPush(c echo.Context) error {
	op := new(crudr.Op)
	if err := c.Bind(op); err != nil {
		return c.String(http.StatusBadRequest, err.Error())
	}

	if v, _ := strconv.ParseBool(env.String("OPENAUDIO_LOG_CRUD_PUSH", "LOG_CRUD_PUSH")); v {
		ss.logger.Debug("CRUD_PUSH", zap.Any("op", op))
	}

	known := ss.crud.KnownType(op)
	if !known {
		return c.String(406, "unknown crudr type")
	}

	return ss.crud.ApplyOp(op)
}
