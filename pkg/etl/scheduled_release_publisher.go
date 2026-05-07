package etl

import (
	"context"
	"time"

	"github.com/OpenAudio/go-openaudio/etl/db"
	"go.uber.org/zap"
)

// ScheduledReleasePublisher periodically publishes scheduled tracks and
// albums whose release_date has passed. Mirrors apps' publish_scheduled_releases
// celery task.
//
// - tracks: is_unlisted=true → false when is_scheduled_release and
//   release_date < now().
// - playlists: is_private=true → false when is_album AND is_scheduled_release
//   AND release_date < now(). (apps only releases albums for now.)
type ScheduledReleasePublisher struct {
	db       db.DBTX
	logger   *zap.Logger
	interval time.Duration
	done     chan bool
}

// NewScheduledReleasePublisher creates a publisher that runs every minute.
func NewScheduledReleasePublisher(database db.DBTX, logger *zap.Logger) *ScheduledReleasePublisher {
	return &ScheduledReleasePublisher{
		db:       database,
		logger:   logger.With(zap.String("service", "scheduled_release_publisher")),
		interval: 60 * time.Second,
		done:     make(chan bool),
	}
}

// Start runs the publish loop. Blocks; intended to run in a goroutine.
func (p *ScheduledReleasePublisher) Start(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	p.logger.Info("Starting scheduled release publisher", zap.Duration("interval", p.interval))

	p.publish(ctx)

	for {
		select {
		case <-ticker.C:
			p.publish(ctx)
		case <-p.done:
			p.logger.Info("Scheduled release publisher stopped via done channel")
			return nil
		case <-ctx.Done():
			p.logger.Info("Scheduled release publisher stopped via context cancellation")
			return ctx.Err()
		}
	}
}

// Stop signals the loop to exit.
func (p *ScheduledReleasePublisher) Stop() {
	close(p.done)
	p.logger.Info("Stopped scheduled release publisher")
}

func (p *ScheduledReleasePublisher) publish(ctx context.Context) {
	start := time.Now()

	tracksRes, err := p.db.Exec(ctx, `
		UPDATE tracks
		SET is_unlisted = false, updated_at = now()
		WHERE is_unlisted = true
		  AND is_scheduled_release = true
		  AND release_date IS NOT NULL
		  AND release_date < now()
		  AND is_current = true
		  AND is_delete = false
	`)
	if err != nil {
		p.logger.Error("publish scheduled tracks failed", zap.Error(err))
	}

	playlistsRes, err := p.db.Exec(ctx, `
		UPDATE playlists
		SET is_private = false, updated_at = now()
		WHERE is_private = true
		  AND is_album = true
		  AND is_scheduled_release = true
		  AND release_date IS NOT NULL
		  AND release_date < now()
		  AND is_current = true
		  AND is_delete = false
	`)
	if err != nil {
		p.logger.Error("publish scheduled playlists failed", zap.Error(err))
	}

	tracksCount := tracksRes.RowsAffected()
	playlistsCount := playlistsRes.RowsAffected()

	if tracksCount > 0 || playlistsCount > 0 {
		p.logger.Info("published scheduled releases",
			zap.Int64("tracks", tracksCount),
			zap.Int64("albums", playlistsCount),
			zap.Duration("duration", time.Since(start)))
	}
}
