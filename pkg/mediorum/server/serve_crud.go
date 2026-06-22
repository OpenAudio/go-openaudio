package server

import (
	"bytes"
	"context"
	"encoding/json"
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
const crudSweepMaxResponseBytes = 64 << 20

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

type crudSweepResponse struct {
	body            []byte
	lastScannedULID string
	limited         bool
	scannedRows     int
	responseRows    int
}

func (ss *MediorumServer) serveCrudSweep(c echo.Context) error {
	ss.crudSweepMutex.Lock()
	defer ss.crudSweepMutex.Unlock()

	ctx, cancel := context.WithTimeout(c.Request().Context(), 1*time.Minute)
	defer cancel()

	sweep, err := ss.buildCrudSweepResponse(ctx, c.QueryParam("after"), crudSweepMaxResponseBytes)
	if err != nil {
		return c.String(500, fmt.Sprintf("Failed to build crud sweep: %v", err))
	}

	c.Response().Header().Set(echo.HeaderCacheControl, "public, max-age=300")
	if sweep.lastScannedULID != "" {
		c.Response().Header().Set(crudr.SweepLastScannedULIDHeader, sweep.lastScannedULID)
	}
	if sweep.limited {
		c.Response().Header().Set(crudr.SweepLimitedHeader, "true")
	}
	return c.Blob(200, echo.MIMEApplicationJSONCharsetUTF8, sweep.body)
}

func (ss *MediorumServer) buildCrudSweepResponse(ctx context.Context, after string, maxResponseBytes int) (crudSweepResponse, error) {
	if maxResponseBytes <= 0 {
		maxResponseBytes = crudSweepMaxResponseBytes
	}

	rows, err := ss.crud.DB.
		WithContext(ctx).
		Model(&crudr.Op{}).
		Select("ulid, host, action, \"table\", data").
		Where("ulid > ?", after).
		Limit(PullLimit).
		Order("ulid asc").
		Rows()
	if err != nil {
		return crudSweepResponse{}, err
	}
	defer rows.Close()

	var sweep crudSweepResponse
	var body bytes.Buffer
	body.WriteByte('[')

	myHost := []byte(ss.Config.Self.Host)
	for rows.Next() {
		var op crudr.Op
		if err := ss.crud.DB.ScanRows(rows, &op); err != nil {
			return crudSweepResponse{}, err
		}
		sweep.scannedRows++

		// Some peers can't talk to each other, so only forward upload ops for
		// which this node is an original upload mirror.
		if op.Table == "uploads" && !bytes.Contains(op.Data, myHost) {
			sweep.lastScannedULID = op.ULID
			continue
		}

		payload, err := json.Marshal(&op)
		if err != nil {
			return crudSweepResponse{}, err
		}

		nextBytes := len(payload)
		if sweep.responseRows > 0 {
			nextBytes++ // comma separator
		}
		if sweep.responseRows > 0 && body.Len()+nextBytes+1 > maxResponseBytes {
			sweep.limited = true
			break
		}

		if sweep.responseRows > 0 {
			body.WriteByte(',')
		}
		body.Write(payload)
		sweep.responseRows++
		sweep.lastScannedULID = op.ULID
	}
	if err := rows.Err(); err != nil {
		return crudSweepResponse{}, err
	}

	if sweep.scannedRows == PullLimit {
		sweep.limited = true
	}

	body.WriteByte(']')
	sweep.body = body.Bytes()

	return sweep, nil
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
		MaxResponseBytes     int                        `json:"max_response_bytes"`
		AudioAnalysisBacklog audioAnalysisBacklogStatus `json:"audio_analysis_backlog"`
	}{
		Status:               status,
		PullLimit:            PullLimit,
		MaxResponseBytes:     crudSweepMaxResponseBytes,
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
