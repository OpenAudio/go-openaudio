package server

import (
	"context"
	"time"

	"go.uber.org/zap"
)

type TranscodeStats struct {
	Since              time.Time `db:"since"`
	UploadCount        int       `db:"upload_count"`
	MinTranscodeTime   float64   `db:"min_transcode_time"`
	AvgTranscodeTime   float64   `db:"avg_transcode_time"`
	MaxTranscodeTime   float64   `db:"max_transcode_time"`
	TotalTranscodeTime float64   `db:"total_transcode_time"`
	TotalBytes         int       `db:"total_bytes"`
	TranscodeRate      float64   `db:"transcode_rate"`
}

func (ss *MediorumServer) updateTranscodeStats(_ context.Context) *TranscodeStats {
	var stats *TranscodeStats
	err := ss.crud.DB.Raw(`
	select
		NOW() - INTERVAL '10 days' as since,
		count(*) as upload_count,
		extract(epoch FROM min(transcoded_at - created_at)) as min_transcode_time,
		extract(epoch FROM avg(transcoded_at - created_at)) as avg_transcode_time,
		extract(epoch FROM max(transcoded_at - created_at)) as max_transcode_time,
		extract(epoch FROM sum(transcoded_at - created_at)) as total_transcode_time,
		sum((ff_probe::json->'format'->>'size')::int) as total_bytes,
		sum((ff_probe::json->'format'->>'size')::int) / extract(epoch FROM sum(transcoded_at - created_at)) as transcode_rate
	from uploads
	where template = 'audio'
		and created_at > NOW() - INTERVAL '10 days'
		and created_by = transcoded_by
		and created_by = ?
	`, ss.Config.Self.Host).Scan(&stats).Error

	if err != nil {
		ss.logger.Error("transcode stats query failed", zap.Error(err))
	}

	// set pointer
	ss.statsMutex.Lock()
	ss.transcodeStats = stats
	ss.statsMutex.Unlock()

	return stats
}

func (ss *MediorumServer) getTranscodeStats() *TranscodeStats {
	ss.statsMutex.RLock()
	defer ss.statsMutex.RUnlock()
	return ss.transcodeStats
}
