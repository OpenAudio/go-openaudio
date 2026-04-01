package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Diff compares domain table state after the ETL ran against the snapshot baseline.
func Diff(ctx context.Context, pool *pgxpool.Pool) error {
	// Read snapshot metadata
	var maxBlock int64
	err := pool.QueryRow(ctx,
		`SELECT value_int FROM _parity_meta WHERE key = 'max_block'`).Scan(&maxBlock)
	if err != nil {
		return fmt.Errorf("reading snapshot max_block (did you run 'snapshot' first?): %w", err)
	}

	var snapshotTime string
	err = pool.QueryRow(ctx,
		`SELECT value_text FROM _parity_meta WHERE key = 'snapshot_time'`).Scan(&snapshotTime)
	if err != nil {
		return fmt.Errorf("reading snapshot_time: %w", err)
	}

	// Get current max block (after ETL ran)
	var currentMaxBlock int64
	pool.QueryRow(ctx, `SELECT COALESCE(MAX(number), 0) FROM blocks`).Scan(&currentMaxBlock)

	// Get max ETL block
	var etlMaxBlock int64
	pool.QueryRow(ctx, `SELECT COALESCE(MAX(block_height), 0) FROM etl_blocks`).Scan(&etlMaxBlock)

	fmt.Printf("=== ETL Parity Report ===\n")
	fmt.Printf("Snapshot block:  %d\n", maxBlock)
	fmt.Printf("Current block:   %d (blocks table)\n", currentMaxBlock)
	fmt.Printf("ETL max block:   %d (etl_blocks table)\n", etlMaxBlock)
	fmt.Printf("Blocks indexed:  %d\n", etlMaxBlock-maxBlock)
	fmt.Printf("Snapshot time:   %s\n\n", snapshotTime)

	// --- Raw Transaction Counts ---
	fmt.Printf("--- Raw ManageEntity Transactions (block > %d) ---\n", maxBlock)
	rows, err := pool.Query(ctx, `
		SELECT entity_type, action, COUNT(*)
		FROM etl_manage_entities
		WHERE block_height > $1
		GROUP BY entity_type, action
		ORDER BY COUNT(*) DESC
	`, maxBlock)
	if err != nil {
		return fmt.Errorf("query etl_manage_entities: %w", err)
	}
	defer rows.Close()

	txCounts := map[string]int64{}
	var totalTxs int64
	fmt.Printf("%-20s %-15s %10s\n", "EntityType", "Action", "Count")
	fmt.Printf("%-20s %-15s %10s\n", "----------", "------", "-----")
	for rows.Next() {
		var entityType, action string
		var count int64
		if err := rows.Scan(&entityType, &action, &count); err != nil {
			return err
		}
		key := entityType + "/" + action
		txCounts[key] = count
		totalTxs += count
		fmt.Printf("%-20s %-15s %10d\n", entityType, action, count)
	}
	fmt.Printf("%-20s %-15s %10d\n\n", "TOTAL", "", totalTxs)

	// --- Domain Table Growth ---
	fmt.Printf("--- Domain Table Growth (blocknumber > %d) ---\n", maxBlock)
	fmt.Printf("%-30s %10s %10s %10s\n", "Table", "Baseline", "New Rows", "Total Now")
	fmt.Printf("%-30s %10s %10s %10s\n", "-----", "--------", "--------", "---------")

	for _, t := range tables {
		// Get baseline from snapshot
		var baseline int64
		err := pool.QueryRow(ctx,
			`SELECT COALESCE(value_int, 0) FROM _parity_meta WHERE key = $1`,
			"count_"+t.Name).Scan(&baseline)
		if err != nil {
			baseline = -1
		}

		// Count total rows now
		totalNow, err := countRows(ctx, pool, t.Name, "")
		if err != nil {
			fmt.Printf("%-30s %10s %10s %10s (table may not exist)\n", t.Name, "ERR", "-", "-")
			continue
		}

		// Count new rows (added since snapshot)
		var newRows int64
		if t.BlocknumCol != "" {
			newRows, _ = countRows(ctx, pool, t.Name, fmt.Sprintf("%s > %d", t.BlocknumCol, maxBlock))
		} else if t.CreatedAtCol != "" {
			newRows, _ = countRows(ctx, pool, t.Name, fmt.Sprintf("%s > '%s'", t.CreatedAtCol, snapshotTime))
		}

		fmt.Printf("%-30s %10d %10d %10d\n", t.Name, baseline, newRows, totalNow)
	}

	// --- Structural Integrity Checks ---
	fmt.Println()
	fmt.Printf("--- Structural Integrity (new rows, blocknumber > %d) ---\n", maxBlock)

	checks := []struct {
		name  string
		query string
	}{
		{
			"users: missing handle",
			fmt.Sprintf("SELECT COUNT(*) FROM users WHERE blocknumber > %d AND is_current = true AND (handle IS NULL OR handle = '')", maxBlock),
		},
		{
			"users: missing wallet",
			fmt.Sprintf("SELECT COUNT(*) FROM users WHERE blocknumber > %d AND is_current = true AND (wallet IS NULL OR wallet = '')", maxBlock),
		},
		{
			"tracks: missing owner_id",
			fmt.Sprintf("SELECT COUNT(*) FROM tracks WHERE blocknumber > %d AND is_current = true AND owner_id = 0", maxBlock),
		},
		{
			"tracks: owner not in users",
			fmt.Sprintf(`SELECT COUNT(*) FROM tracks t WHERE t.blocknumber > %d AND t.is_current = true
				AND NOT EXISTS (SELECT 1 FROM users u WHERE u.user_id = t.owner_id AND u.is_current = true)`, maxBlock),
		},
		{
			"follows: followee not in users",
			fmt.Sprintf(`SELECT COUNT(*) FROM follows f WHERE f.blocknumber > %d AND f.is_current = true AND f.is_delete = false
				AND NOT EXISTS (SELECT 1 FROM users u WHERE u.user_id = f.followee_user_id AND u.is_current = true)`, maxBlock),
		},
		{
			"saves: target track/playlist missing",
			fmt.Sprintf(`SELECT COUNT(*) FROM saves s WHERE s.blocknumber > %d AND s.is_current = true AND s.is_delete = false
				AND s.save_type = 'track'
				AND NOT EXISTS (SELECT 1 FROM tracks t WHERE t.track_id = s.save_item_id AND t.is_current = true)`, maxBlock),
		},
	}

	allPassed := true
	for _, c := range checks {
		var count int64
		err := pool.QueryRow(ctx, c.query).Scan(&count)
		if err != nil {
			fmt.Printf("  %-45s  ERR (%v)\n", c.name, err)
			continue
		}
		status := "✓"
		if count > 0 {
			status = fmt.Sprintf("WARN: %d", count)
			allPassed = false
		}
		fmt.Printf("  %-45s  %s\n", c.name, status)
	}

	if allPassed {
		fmt.Println("\nAll structural checks passed.")
	} else {
		fmt.Println("\nSome structural checks have warnings — review above.")
	}

	// --- Unhandled Transaction Types ---
	fmt.Println()
	fmt.Printf("--- Unhandled ManageEntity Types (block > %d) ---\n", maxBlock)

	// Find entity_type/action combos that have etl_manage_entities rows
	// but zero corresponding domain table rows
	unhandledRows, err := pool.Query(ctx, `
		SELECT entity_type, action, COUNT(*)
		FROM etl_manage_entities
		WHERE block_height > $1
		GROUP BY entity_type, action
		HAVING COUNT(*) > 0
		ORDER BY COUNT(*) DESC
	`, maxBlock)
	if err == nil {
		defer unhandledRows.Close()

		// Build set of handled types from the tables list
		handled := map[string]bool{}
		for _, t := range tables {
			if t.EntityType != "" && t.Action != "" {
				handled[strings.ToLower(t.EntityType+"/"+t.Action)] = true
			}
		}

		hasUnhandled := false
		for unhandledRows.Next() {
			var entityType, action string
			var count int64
			unhandledRows.Scan(&entityType, &action, &count)
			key := strings.ToLower(entityType + "/" + action)
			if !handled[key] {
				if !hasUnhandled {
					fmt.Printf("%-20s %-15s %10s\n", "EntityType", "Action", "Count")
					fmt.Printf("%-20s %-15s %10s\n", "----------", "------", "-----")
					hasUnhandled = true
				}
				fmt.Printf("%-20s %-15s %10d\n", entityType, action, count)
			}
		}
		if !hasUnhandled {
			fmt.Println("(none — all transaction types are handled)")
		}
	}

	fmt.Println("\n=== Report Complete ===")
	return nil
}
