package etl

import (
	"context"
	"fmt"
	"time"

	"github.com/AudiusProject/audiusd/pkg/etl/db"
	"go.uber.org/zap"
)

// MaterializedViewRefresher refreshes dashboard materialized views periodically
type MaterializedViewRefresher struct {
	db     db.DBTX
	logger *zap.Logger
	ticker *time.Ticker
	done   chan bool
}

// NewMaterializedViewRefresher creates a new refresher service
func NewMaterializedViewRefresher(database db.DBTX, logger *zap.Logger) *MaterializedViewRefresher {
	return &MaterializedViewRefresher{
		db:     database,
		logger: logger.With(zap.String("service", "mv_refresher")),
		done:   make(chan bool),
	}
}

// Start begins the periodic refresh cycle (every 2 minutes)
// This method blocks and should be run in a goroutine (e.g., via errgroup)
func (r *MaterializedViewRefresher) Start(ctx context.Context) error {
	r.ticker = time.NewTicker(2 * time.Minute)
	defer r.ticker.Stop()

	r.logger.Info("Starting materialized view refresher", zap.String("interval", "2m"))

	// Initial refresh on startup
	r.refreshViews(ctx)

	for {
		select {
		case <-r.ticker.C:
			r.refreshViews(ctx)
		case <-r.done:
			r.logger.Info("Materialized view refresher stopped via done channel")
			return nil
		case <-ctx.Done():
			r.logger.Info("Materialized view refresher stopped via context cancellation")
			return ctx.Err()
		}
	}
}

// Stop stops the refresher
func (r *MaterializedViewRefresher) Stop() {
	if r.ticker != nil {
		r.ticker.Stop()
	}
	close(r.done)
	r.logger.Info("Stopped materialized view refresher")
}

// refreshViews calls the database function to refresh all dashboard materialized views
// It attempts to refresh independent views in parallel for better performance
func (r *MaterializedViewRefresher) refreshViews(ctx context.Context) {
	start := time.Now()

	// List of materialized views to refresh
	views := []string{
		"mv_dashboard_transaction_stats",
		"mv_dashboard_transaction_types",
	}

	// Refresh views in parallel for better performance
	type result struct {
		view     string
		err      error
		duration time.Duration
	}

	results := make(chan result, len(views))

	for _, view := range views {
		go func(viewName string) {
			viewStart := time.Now()
			_, err := r.db.Exec(ctx, "REFRESH MATERIALIZED VIEW "+viewName)
			results <- result{
				view:     viewName,
				err:      err,
				duration: time.Since(viewStart),
			}
		}(view)
	}

	// Collect results
	var errors []string
	for i := 0; i < len(views); i++ {
		res := <-results
		if res.err != nil {
			r.logger.Error("Failed to refresh materialized view",
				zap.String("view", res.view),
				zap.Error(res.err),
				zap.Duration("duration", res.duration))
			errors = append(errors, fmt.Sprintf("%s: %v", res.view, res.err))
		} else {
			r.logger.Debug("Refreshed materialized view",
				zap.String("view", res.view),
				zap.Duration("duration", res.duration))
		}
	}

	totalDuration := time.Since(start)
	if len(errors) > 0 {
		r.logger.Warn("Some materialized views failed to refresh",
			zap.Int("errors", len(errors)),
			zap.Duration("total_duration", totalDuration))
	} else {
		r.logger.Info("Successfully refreshed all materialized views",
			zap.Int("views", len(views)),
			zap.Duration("total_duration", totalDuration))
	}
}
