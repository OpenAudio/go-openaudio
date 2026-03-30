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
	blocks := flag.Int("blocks", 1, "number of blocks to roll back")
	dryRun := flag.Bool("dry-run", false, "show what would be done without making changes")
	flag.Parse()

	if *cometDataDir == "" || *pgURL == "" {
		fmt.Println("Usage: rollback -comet-data <dir> -pg <postgres-url> [-blocks N] [-dry-run]")
		fmt.Println()
		fmt.Println("Rolls back CometBFT state by N blocks (default 1) and cleans up the")
		fmt.Println("corresponding PG state so the blocks can be replayed cleanly.")
		fmt.Println()
		fmt.Println("Stop the node before running this.")
		os.Exit(1)
	}

	if *blocks < 1 {
		fmt.Fprintln(os.Stderr, "-blocks must be >= 1")
		os.Exit(1)
	}

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

	stateHeight := currentState.LastBlockHeight
	fmt.Printf("Block store height: %d\n", blockStore.Height())
	fmt.Printf("State height:       %d\n", stateHeight)
	fmt.Printf("Will roll back %d block(s): %d -> %d\n\n", *blocks, stateHeight, stateHeight-int64(*blocks))

	if *dryRun {
		for i := range *blocks {
			h := stateHeight - int64(i)
			fmt.Printf("[dry-run] Would roll back block %d and clean up PG\n", h)
		}
		fmt.Println("\nRun without -dry-run to execute.")
		return
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, *pgURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to postgres: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	for i := range *blocks {
		rollbackTarget := stateHeight - int64(i)
		fmt.Printf("--- Rolling back block %d (%d/%d) ---\n", rollbackTarget, i+1, *blocks)

		height, hash, err := state.Rollback(blockStore, stateStore, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "CometBFT rollback failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("CometBFT rolled back to height %d (app hash: %X)\n", height, hash)

		if err := cleanPG(ctx, conn, rollbackTarget); err != nil {
			fmt.Fprintf(os.Stderr, "PG cleanup failed for block %d: %v\n", rollbackTarget, err)
			fmt.Fprintln(os.Stderr, "Clean up PG manually:")
			printPGCleanup(rollbackTarget)
			os.Exit(1)
		}
		fmt.Println()
	}

	replayFrom := stateHeight - int64(*blocks) + 1
	fmt.Println("Rollback complete.")
	fmt.Printf("It will replay from block %d with the new code.\n", replayFrom)
}

func cleanPG(ctx context.Context, conn *pgx.Conn, height int64) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %v", err)
	}

	queries := []struct {
		desc string
		sql  string
	}{
		{"core_blocks", fmt.Sprintf("DELETE FROM core_blocks WHERE height = %d", height)},
		{"core_transactions", fmt.Sprintf("DELETE FROM core_transactions WHERE block_id = %d", height)},
		{"core_tx_stats", fmt.Sprintf("DELETE FROM core_tx_stats WHERE block_height = %d", height)},
		{"validator_history", fmt.Sprintf("DELETE FROM validator_history WHERE event_block = %d", height)},
		{"sla_node_reports (uncommitted)", "DELETE FROM sla_node_reports WHERE sla_rollup_id IS NULL"},
		{"core_app_state", fmt.Sprintf("DELETE FROM core_app_state WHERE block_height = %d", height)},
	}

	for _, q := range queries {
		tag, err := tx.Exec(ctx, q.sql)
		if err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("failed to clean %s: %v", q.desc, err)
		}
		fmt.Printf("  PG: cleaned %-30s (%d rows affected)\n", q.desc, tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit: %v", err)
	}
	return nil
}

func printPGCleanup(height int64) {
	fmt.Fprintf(os.Stderr, "\n  DELETE FROM core_blocks WHERE height = %d;\n", height)
	fmt.Fprintf(os.Stderr, "  DELETE FROM core_transactions WHERE block_id = %d;\n", height)
	fmt.Fprintf(os.Stderr, "  DELETE FROM core_tx_stats WHERE block_height = %d;\n", height)
	fmt.Fprintf(os.Stderr, "  DELETE FROM validator_history WHERE event_block = %d;\n", height)
	fmt.Fprintf(os.Stderr, "  DELETE FROM sla_node_reports WHERE sla_rollup_id IS NULL;\n")
	fmt.Fprintf(os.Stderr, "  DELETE FROM core_app_state WHERE block_height = %d;\n", height)
}
