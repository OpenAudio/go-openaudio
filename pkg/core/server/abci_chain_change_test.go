package server

import (
	"strings"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/core/db"
)

func TestIsForeignChainBlock(t *testing.T) {
	const genesis = "audius-mainnet-beta"
	for _, tc := range []struct {
		name    string
		chainID string
		want    bool
	}{
		// A node moving to the new chain: its PostgreSQL still holds the old
		// chain's blocks under the new binary's genesis.
		{name: "previous chain", chainID: "audius-mainnet-alpha-beta", want: true},
		// A restore of this chain's own state must not be reset; it is
		// checkCometDataFiles' job to refuse that if the chain files are missing.
		{name: "same chain", chainID: genesis, want: false},
		// Not a positive identification of another chain, so never grounds for
		// discarding state.
		{name: "no chain id recorded", chainID: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := isForeignChainBlock(db.CoreBlock{Height: 34_000_000, ChainID: tc.chainID}, genesis)
			if got != tc.want {
				t.Errorf("isForeignChainBlock(chain_id=%q) = %v, want %v", tc.chainID, got, tc.want)
			}
		})
	}
}

func TestForeignChainResetStmtClearsCoreConsensusState(t *testing.T) {
	existing := append([]string{}, stateSyncSnapshotTables...)
	existing = append(existing, mediorumTables...)
	existing = append(existing, "etl_blocks", "etl_plays")

	stmt := foreignChainResetStmt(existing)
	if stmt == "" {
		t.Fatal("expected a TRUNCATE statement when core tables exist")
	}

	// Emptying these is what makes startABCI treat the node as unsynced and
	// CometBFT's handshake see app height 0. The rest of the set must go too, or
	// a node that block-syncs replays the new chain over the old chain's
	// validators and auth state.
	for _, table := range stateSyncSnapshotTables {
		if table == "core_db_migrations" {
			continue
		}
		if !strings.Contains(stmt, `"`+table+`"`) {
			t.Errorf("reset does not clear %q, so the old chain's rows would survive into the new one", table)
		}
	}
}

func TestForeignChainResetStmtKeepsMigrationsAndNonCoreTables(t *testing.T) {
	existing := append([]string{}, stateSyncSnapshotTables...)
	existing = append(existing, mediorumTables...)
	existing = append(existing, "etl_blocks", "etl_plays")

	stmt := foreignChainResetStmt(existing)

	// Unlike a restore, nothing reloads core_db_migrations afterwards; clearing
	// it would make the next startup re-run every migration against existing
	// tables.
	if strings.Contains(stmt, `"core_db_migrations"`) {
		t.Error("reset must not clear core_db_migrations")
	}
	for _, table := range append(append([]string{}, mediorumTables...), "etl_blocks", "etl_plays") {
		if strings.Contains(stmt, `"`+table+`"`) {
			t.Errorf("reset names %q, which belongs to another service and no chain change invalidates", table)
		}
	}
	if strings.Contains(strings.ToUpper(stmt), "CASCADE") {
		t.Error("reset must not cascade into tables outside the core set")
	}
}
