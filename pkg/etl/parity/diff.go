package main

import (
	"context"
	"fmt"

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

	// Determine em_block boundary (domain tables use em_block, not chain height)
	var emBlockBoundary int64
	err = pool.QueryRow(ctx,
		`SELECT COALESCE(em_block, $1) FROM core_indexed_blocks
		 WHERE height = $1 ORDER BY height DESC LIMIT 1`, maxBlock).Scan(&emBlockBoundary)
	if err != nil {
		// Fallback: compute from offset
		var offset int64
		err2 := pool.QueryRow(ctx,
			`SELECT em_block - height FROM core_indexed_blocks ORDER BY height DESC LIMIT 1`).Scan(&offset)
		if err2 != nil {
			emBlockBoundary = maxBlock // last resort: use chain height directly
		} else {
			emBlockBoundary = maxBlock + offset
		}
	}

	// Get current max block
	var currentMaxBlock int64
	pool.QueryRow(ctx, `SELECT COALESCE(MAX(number), 0) FROM blocks`).Scan(&currentMaxBlock)

	var etlMaxBlock int64
	pool.QueryRow(ctx, `SELECT COALESCE(MAX(block_height), 0) FROM etl_blocks`).Scan(&etlMaxBlock)

	fmt.Printf("=== ETL Parity Report ===\n")
	fmt.Printf("Snapshot chain height: %d\n", maxBlock)
	fmt.Printf("Snapshot em_block:     %d\n", emBlockBoundary)
	fmt.Printf("ETL max chain height:  %d (etl_blocks)\n", etlMaxBlock)
	fmt.Printf("Blocks indexed:        %d\n", etlMaxBlock-maxBlock)
	fmt.Printf("Snapshot time:         %s\n\n", snapshotTime)

	// --- Raw Transaction Counts ---
	fmt.Printf("--- ManageEntity Transactions (chain height > %d) ---\n", maxBlock)
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
	fmt.Printf("%-20s %-20s %10s\n", "EntityType", "Action", "Count")
	fmt.Printf("%-20s %-20s %10s\n", "----------", "------", "-----")
	for rows.Next() {
		var entityType, action string
		var count int64
		if err := rows.Scan(&entityType, &action, &count); err != nil {
			return err
		}
		key := entityType + "/" + action
		txCounts[key] = count
		totalTxs += count
		fmt.Printf("%-20s %-20s %10d\n", entityType, action, count)
	}
	fmt.Printf("%-20s %-20s %10d\n\n", "TOTAL", "", totalTxs)

	// --- Domain Table Growth ---
	fmt.Printf("--- Domain Table Growth (em_block > %d) ---\n", emBlockBoundary)
	fmt.Printf("%-30s %10s %10s %10s\n", "Table", "Baseline", "New Rows", "Total Now")
	fmt.Printf("%-30s %10s %10s %10s\n", "-----", "--------", "--------", "---------")

	for _, t := range tables {
		var baseline int64
		err := pool.QueryRow(ctx,
			`SELECT COALESCE(value_int, 0) FROM _parity_meta WHERE key = $1`,
			"count_"+t.Name).Scan(&baseline)
		if err != nil {
			baseline = -1
		}

		totalNow, err := countRows(ctx, pool, t.Name, "")
		if err != nil {
			fmt.Printf("%-30s %10s %10s %10s (error)\n", t.Name, "ERR", "-", "-")
			continue
		}

		var newRows int64
		if t.BlocknumCol != "" {
			newRows, _ = countRows(ctx, pool, t.Name, fmt.Sprintf("%s > %d", t.BlocknumCol, emBlockBoundary))
		} else if t.CreatedAtCol != "" {
			newRows, _ = countRows(ctx, pool, t.Name, fmt.Sprintf("%s > '%s'", t.CreatedAtCol, snapshotTime))
		}

		fmt.Printf("%-30s %10d %10d %10d\n", t.Name, baseline, newRows, totalNow)
	}

	// --- Structural Integrity Checks ---
	fmt.Println()
	fmt.Printf("--- Structural Integrity (new rows, em_block > %d) ---\n", emBlockBoundary)

	checks := []struct {
		name  string
		query string
	}{
		{
			"users: missing handle",
			fmt.Sprintf("SELECT COUNT(*) FROM users WHERE blocknumber > %d AND is_current = true AND (handle IS NULL OR handle = '')", emBlockBoundary),
		},
		{
			"users: missing wallet",
			fmt.Sprintf("SELECT COUNT(*) FROM users WHERE blocknumber > %d AND is_current = true AND (wallet IS NULL OR wallet = '')", emBlockBoundary),
		},
		{
			"tracks: missing title",
			fmt.Sprintf("SELECT COUNT(*) FROM tracks WHERE blocknumber > %d AND is_current = true AND is_delete = false AND (title IS NULL OR title = '')", emBlockBoundary),
		},
		{
			"tracks: missing owner_id",
			fmt.Sprintf("SELECT COUNT(*) FROM tracks WHERE blocknumber > %d AND is_current = true AND owner_id = 0", emBlockBoundary),
		},
		{
			"tracks: owner not in users",
			fmt.Sprintf(`SELECT COUNT(*) FROM tracks t WHERE t.blocknumber > %d AND t.is_current = true AND t.is_delete = false
				AND NOT EXISTS (SELECT 1 FROM users u WHERE u.user_id = t.owner_id AND u.is_current = true)`, emBlockBoundary),
		},
		{
			"follows: followee not in users",
			fmt.Sprintf(`SELECT COUNT(*) FROM follows f WHERE f.blocknumber > %d AND f.is_current = true AND f.is_delete = false
				AND NOT EXISTS (SELECT 1 FROM users u WHERE u.user_id = f.followee_user_id AND u.is_current = true)`, emBlockBoundary),
		},
		{
			"saves: target track missing",
			fmt.Sprintf(`SELECT COUNT(*) FROM saves s WHERE s.blocknumber > %d AND s.is_current = true AND s.is_delete = false
				AND s.save_type = 'track'
				AND NOT EXISTS (SELECT 1 FROM tracks t WHERE t.track_id = s.save_item_id AND t.is_current = true)`, emBlockBoundary),
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

	// --- Match Rates ---
	fmt.Println()
	fmt.Printf("--- Match Rates (txs vs new domain rows) ---\n")
	fmt.Printf("%-30s %10s %10s %10s\n", "Type", "Txs", "New Rows", "Rate")
	fmt.Printf("%-30s %10s %10s %10s\n", "----", "---", "--------", "----")

	matchChecks := []struct {
		label    string
		txKey    string
		table    string
		whereCol string
	}{
		{"User Create", "User/Create", "users", "blocknumber"},
		{"Track Create", "Track/Create", "tracks", "blocknumber"},
		{"Playlist Create", "Playlist/Create", "playlists", "blocknumber"},
		{"Follow", "User/Follow", "follows", "blocknumber"},
		{"Save (track)", "Track/Save", "saves", "blocknumber"},
		{"Repost (track)", "Track/Repost", "reposts", "blocknumber"},
		{"Subscribe", "User/Subscribe", "subscriptions", "blocknumber"},
		{"Share (track)", "Track/Share", "shares", "blocknumber"},
		{"Comment Create", "Comment/Create", "comments", "blocknumber"},
		{"Track Download", "Track/Download", "track_downloads", "blocknumber"},
		{"Grant Create", "Grant/Create", "grants", "blocknumber"},
	}

	for _, mc := range matchChecks {
		txCount := txCounts[mc.txKey]
		if txCount == 0 {
			continue
		}
		newRows, _ := countRows(ctx, pool, mc.table, fmt.Sprintf("%s > %d", mc.whereCol, emBlockBoundary))
		rate := ""
		if txCount > 0 {
			pct := float64(newRows) / float64(txCount) * 100
			rate = fmt.Sprintf("%.0f%%", pct)
		}
		fmt.Printf("%-30s %10d %10d %10s\n", mc.label, txCount, newRows, rate)
	}

	fmt.Println("\n=== Report Complete ===")
	return nil
}
