package server

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pollCopyProgress queries pg_stat_progress_copy on a fixed interval and
// calls update with a human-readable status string until ctx is cancelled.
// It is a no-op when no COPY FROM is active (returns empty string).
func pollCopyProgress(ctx context.Context, pool *pgxpool.Pool, interval time.Duration, update func(string)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if msg := queryCopyProgress(ctx, pool); msg != "" {
				update(msg)
			}
		}
	}
}

// queryCopyProgress returns a formatted progress string for the most active
// COPY FROM in progress, or "" if none is running or the query fails.
func queryCopyProgress(ctx context.Context, pool *pgxpool.Pool) string {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return ""
	}
	defer conn.Release()

	var relname string
	var bytesProcessed, bytesTotal int64
	err = conn.QueryRow(ctx, `
		SELECT c.relname, p.bytes_processed, p.bytes_total
		FROM pg_stat_progress_copy p
		JOIN pg_class c ON c.oid = p.relid
		WHERE p.command = 'COPY FROM'
		ORDER BY p.bytes_processed DESC
		LIMIT 1
	`).Scan(&relname, &bytesProcessed, &bytesTotal)
	if err != nil {
		return ""
	}

	processed := float64(bytesProcessed) / (1 << 30)
	if bytesTotal > 0 {
		total := float64(bytesTotal) / (1 << 30)
		pct := int(100 * bytesProcessed / bytesTotal)
		return fmt.Sprintf("%s — %.1f / %.1f GB (%d%%)", relname, processed, total, pct)
	}
	return fmt.Sprintf("%s — %.1f GB", relname, processed)
}
