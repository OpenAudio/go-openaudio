package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Snapshot captures the current state of domain tables and the max block height.
func Snapshot(ctx context.Context, pool *pgxpool.Pool) error {
	// Drop and recreate meta table (idempotent)
	_, err := pool.Exec(ctx, `DROP TABLE IF EXISTS _parity_meta`)
	if err != nil {
		return fmt.Errorf("drop _parity_meta: %w", err)
	}

	_, err = pool.Exec(ctx, `
		CREATE TABLE _parity_meta (
			key TEXT PRIMARY KEY,
			value_int BIGINT,
			value_text TEXT,
			created_at TIMESTAMP NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("create _parity_meta: %w", err)
	}

	// Record max chain block height from core_indexed_blocks (the CometBFT block height)
	var maxBlock int64
	err = pool.QueryRow(ctx, `SELECT COALESCE(MAX(height), 0) FROM core_indexed_blocks`).Scan(&maxBlock)
	if err != nil {
		return fmt.Errorf("query max block: %w", err)
	}

	_, err = pool.Exec(ctx,
		`INSERT INTO _parity_meta (key, value_int) VALUES ('max_block', $1)`,
		maxBlock)
	if err != nil {
		return fmt.Errorf("insert max_block: %w", err)
	}

	// Record snapshot time
	_, err = pool.Exec(ctx,
		`INSERT INTO _parity_meta (key, value_text) VALUES ('snapshot_time', $1)`,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("insert snapshot_time: %w", err)
	}

	fmt.Printf("=== Parity Snapshot ===\n")
	fmt.Printf("Max block: %d\n", maxBlock)
	fmt.Printf("Snapshot time: %s\n\n", time.Now().UTC().Format(time.RFC3339))

	// Count rows in each domain table
	fmt.Printf("%-30s %10s\n", "Table", "Rows")
	fmt.Printf("%-30s %10s\n", "-----", "----")

	for _, t := range tables {
		count, err := countRows(ctx, pool, t.Name, "")
		if err != nil {
			fmt.Printf("%-30s %10s (table may not exist)\n", t.Name, "ERR")
			continue
		}

		_, err = pool.Exec(ctx,
			`INSERT INTO _parity_meta (key, value_int) VALUES ($1, $2)`,
			"count_"+t.Name, count)
		if err != nil {
			return fmt.Errorf("insert count for %s: %w", t.Name, err)
		}

		fmt.Printf("%-30s %10d\n", t.Name, count)
	}

	fmt.Printf("\nSnapshot complete. Run the ETL, then use 'diff' to compare.\n")
	return nil
}

func countRows(ctx context.Context, pool *pgxpool.Pool, table, where string) (int64, error) {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
	if where != "" {
		query += " WHERE " + where
	}
	var count int64
	err := pool.QueryRow(ctx, query).Scan(&count)
	return count, err
}
