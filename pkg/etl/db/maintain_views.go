package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MaterializedViewManager handles refreshing materialized views for optimal performance
type MaterializedViewManager struct {
	db     *pgxpool.Pool
	logger *slog.Logger
}

// NewMaterializedViewManager creates a new manager for materialized views
func NewMaterializedViewManager(db *pgxpool.Pool, logger *slog.Logger) *MaterializedViewManager {
	return &MaterializedViewManager{
		db:     db,
		logger: logger,
	}
}

// RefreshSlaRollupViews refreshes all SLA rollup related materialized views
func (m *MaterializedViewManager) RefreshSlaRollupViews(ctx context.Context) error {
	start := time.Now()

	// Use the database function we created in the migration
	_, err := m.db.Exec(ctx, "SELECT refresh_sla_rollup_materialized_views()")
	if err != nil {
		m.logger.Error("Failed to refresh SLA rollup materialized views", "error", err, "duration", time.Since(start))
		return fmt.Errorf("failed to refresh SLA rollup materialized views: %w", err)
	}

	// Also refresh the dashboard stats view
	_, err = m.db.Exec(ctx, "REFRESH MATERIALIZED VIEW CONCURRENTLY mv_sla_rollup_dashboard_stats")
	if err != nil {
		m.logger.Error("Failed to refresh dashboard stats materialized view", "error", err, "duration", time.Since(start))
		return fmt.Errorf("failed to refresh dashboard stats materialized view: %w", err)
	}

	m.logger.Info("Successfully refreshed SLA rollup materialized views", "duration", time.Since(start))
	return nil
}

// StartPeriodicRefresh starts a background goroutine that refreshes materialized views periodically
func (m *MaterializedViewManager) StartPeriodicRefresh(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()

		// Initial refresh
		if err := m.RefreshSlaRollupViews(ctx); err != nil {
			m.logger.Error("Initial materialized view refresh failed", "error", err)
		}

		for {
			select {
			case <-ctx.Done():
				m.logger.Info("Stopping materialized view refresh due to context cancellation")
				return
			case <-ticker.C:
				if err := m.RefreshSlaRollupViews(ctx); err != nil {
					m.logger.Error("Periodic materialized view refresh failed", "error", err)
				}
			}
		}
	}()

	m.logger.Info("Started periodic materialized view refresh", "interval", interval)
}

// GetViewRefreshStatus returns information about when views were last refreshed
func (m *MaterializedViewManager) GetViewRefreshStatus(ctx context.Context) (map[string]time.Time, error) {
	query := `
		SELECT 
			schemaname || '.' || matviewname as view_name,
			COALESCE(last_refresh, '1970-01-01'::timestamp) as last_refresh
		FROM pg_matviews 
		WHERE matviewname IN ('mv_sla_rollup', 'mv_sla_rollup_score', 'mv_sla_rollup_dashboard_stats')
	`

	rows, err := m.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query materialized view status: %w", err)
	}
	defer rows.Close()

	status := make(map[string]time.Time)
	for rows.Next() {
		var viewName string
		var lastRefresh time.Time
		if err := rows.Scan(&viewName, &lastRefresh); err != nil {
			return nil, fmt.Errorf("failed to scan view status: %w", err)
		}
		status[viewName] = lastRefresh
	}

	return status, nil
}

// ForceRefreshIfStale refreshes views if they haven't been refreshed recently
func (m *MaterializedViewManager) ForceRefreshIfStale(ctx context.Context, maxAge time.Duration) error {
	status, err := m.GetViewRefreshStatus(ctx)
	if err != nil {
		return err
	}

	now := time.Now()
	needsRefresh := false

	for viewName, lastRefresh := range status {
		age := now.Sub(lastRefresh)
		if age > maxAge {
			m.logger.Warn("Materialized view is stale", "view", viewName, "age", age, "max_age", maxAge)
			needsRefresh = true
		}
	}

	if needsRefresh {
		return m.RefreshSlaRollupViews(ctx)
	}

	return nil
}

// Health check for materialized views - useful for monitoring
func (m *MaterializedViewManager) HealthCheck(ctx context.Context) error {
	// Check if views exist and are queryable
	testQueries := []string{
		"SELECT COUNT(*) FROM mv_sla_rollup LIMIT 1",
		"SELECT COUNT(*) FROM mv_sla_rollup_score LIMIT 1",
		"SELECT COUNT(*) FROM mv_sla_rollup_dashboard_stats LIMIT 1",
	}

	for _, query := range testQueries {
		var count int64
		if err := m.db.QueryRow(ctx, query).Scan(&count); err != nil {
			return fmt.Errorf("materialized view health check failed for query %s: %w", query, err)
		}
	}

	return nil
}
