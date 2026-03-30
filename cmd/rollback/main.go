package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	"github.com/jackc/pgx/v5"
)

func main() {
	cometDataDir := flag.String("comet-data", "", "path to CometBFT data directory (e.g. /data/core/data)")
	pgURL := flag.String("pg", "", "postgres connection string (e.g. postgresql://postgres:postgres@localhost:5432/openaudio)")
	dryRun := flag.Bool("dry-run", false, "show what would be done without making changes")
	flag.Parse()

	if *cometDataDir == "" || *pgURL == "" {
		fmt.Println("Usage: rollback -comet-data <dir> -pg <postgres-url> [-dry-run]")
		fmt.Println()
		fmt.Println("Rolls back CometBFT state by one block and cleans up the")
		fmt.Println("corresponding PG state so the block can be replayed cleanly.")
		fmt.Println()
		fmt.Println("Stop the node before running this.")
		os.Exit(1)
	}

	// === CometBFT Rollback ===

	blockStoreDB, err := dbm.NewDB("blockstore", dbm.PebbleDBBackend, *cometDataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open blockstore: %v\n", err)
		os.Exit(1)
	}
	defer blockStoreDB.Close()
	blockStore := store.NewBlockStore(blockStoreDB)

	stateDB, err := dbm.NewDB("state", dbm.PebbleDBBackend, *cometDataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open state db: %v\n", err)
		os.Exit(1)
	}
	defer stateDB.Close()
	stateStore := state.NewStore(stateDB, state.StoreOptions{DiscardABCIResponses: false})

	currentState, err := stateStore.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load state: %v\n", err)
		os.Exit(1)
	}

	blockHeight := blockStore.Height()
	stateHeight := currentState.LastBlockHeight
	rollbackTarget := stateHeight // the block that will be removed
	fmt.Printf("Block store height: %d\n", blockHeight)
	fmt.Printf("State height:       %d\n", stateHeight)
	fmt.Printf("Will roll back block %d and replay it on next start\n\n", rollbackTarget)

	if *dryRun {
		fmt.Println("[dry-run] Would roll back CometBFT state to height", rollbackTarget-1)
		fmt.Println("[dry-run] Would clean up PG rows for block", rollbackTarget)
		fmt.Println()
		fmt.Println("Run without -dry-run to execute.")
		return
	}

	// Roll back CometBFT (hard = also remove the block from blockstore)
	height, hash, err := state.Rollback(blockStore, stateStore, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "CometBFT rollback failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("CometBFT rolled back to height %d (app hash: %X)\n", height, hash)

	// === PG Cleanup ===

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, *pgURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to postgres: %v\n", err)
		fmt.Fprintln(os.Stderr, "CometBFT was already rolled back. Clean up PG manually:")
		printPGCleanup(rollbackTarget)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	tx, err := conn.Begin(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to begin PG transaction: %v\n", err)
		printPGCleanup(rollbackTarget)
		os.Exit(1)
	}

	queries := []struct {
		desc string
		sql  string
	}{
		{"core_blocks", fmt.Sprintf("DELETE FROM core_blocks WHERE height = %d", rollbackTarget)},
		{"core_transactions", fmt.Sprintf("DELETE FROM core_transactions WHERE block_id = %d", rollbackTarget)},
		{"core_tx_stats", fmt.Sprintf("DELETE FROM core_tx_stats WHERE block_height = %d", rollbackTarget)},
		{"validator_history", fmt.Sprintf("DELETE FROM validator_history WHERE event_block = %d", rollbackTarget)},
		{"sla_node_reports (uncommitted)", "DELETE FROM sla_node_reports WHERE sla_rollup_id IS NULL"},
		{"core_app_state", fmt.Sprintf("DELETE FROM core_app_state WHERE block_height = %d", rollbackTarget)},
	}

	for _, q := range queries {
		tag, err := tx.Exec(ctx, q.sql)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to clean %s: %v\n", q.desc, err)
			tx.Rollback(ctx)
			fmt.Fprintln(os.Stderr, "PG transaction rolled back. Clean up PG manually:")
			printPGCleanup(rollbackTarget)
			os.Exit(1)
		}
		fmt.Printf("PG: cleaned %-30s (%d rows affected)\n", q.desc, tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "failed to commit PG cleanup: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("Rollback complete.")
	fmt.Printf("It will replay block %d with the new code.\n", rollbackTarget)
}

func printPGCleanup(height int64) {
	fmt.Fprintf(os.Stderr, "\n  DELETE FROM core_blocks WHERE height = %d;\n", height)
	fmt.Fprintf(os.Stderr, "  DELETE FROM core_transactions WHERE block_id = %d;\n", height)
	fmt.Fprintf(os.Stderr, "  DELETE FROM core_tx_stats WHERE block_height = %d;\n", height)
	fmt.Fprintf(os.Stderr, "  DELETE FROM validator_history WHERE event_block = %d;\n", height)
	fmt.Fprintf(os.Stderr, "  DELETE FROM sla_node_reports WHERE sla_rollup_id IS NULL;\n")
	fmt.Fprintf(os.Stderr, "  DELETE FROM core_app_state WHERE block_height = %d;\n", height)
}
