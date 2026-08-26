package server

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// migrationsDir is where the core schema is defined. Resolved relative to this
// package so the guard needs no database and runs in CI.
const migrationsDir = "../db/sql/migrations"

// snapshotExemptCoreTables are core_* tables deliberately left out of
// stateSyncSnapshotTables. Every entry needs a reason: an omission that is a
// decision belongs here, and an omission that is an oversight should fail.
var snapshotExemptCoreTables = map[string]string{
	// The core_etl_tx* family was dropped by 00021_drop_etl_tables.sql and has
	// had no writer since -- indexing moved to pkg/etl, which owns its own
	// database. They only reappear on a freshly migrated node because
	// 0016_core_etl.sql sorts after 00021 (four digits vs five), and they come
	// up empty and stay empty. Nothing in consensus reads them, so shipping
	// them in every snapshot would cost bytes for no state.
	"core_etl_tx":                            "dropped in 00021, no writer since; ETL moved to pkg/etl",
	"core_etl_tx_duplicates":                 "dropped in 00021, no writer since; ETL moved to pkg/etl",
	"core_etl_tx_manage_entity":              "dropped in 00021, no writer since; ETL moved to pkg/etl",
	"core_etl_tx_plays":                      "dropped in 00021, no writer since; ETL moved to pkg/etl",
	"core_etl_tx_sla_rollup":                 "dropped in 00021, no writer since; ETL moved to pkg/etl",
	"core_etl_tx_storage_proof":              "dropped in 00021, no writer since; ETL moved to pkg/etl",
	"core_etl_tx_storage_proof_verification": "dropped in 00021, no writer since; ETL moved to pkg/etl",
	"core_etl_tx_validator_deregistration":   "dropped in 00021, no writer since; ETL moved to pkg/etl",
	"core_etl_tx_validator_registration":     "dropped in 00021, no writer since; ETL moved to pkg/etl",
}

// Every core_* table in the schema must either ship in a state-sync snapshot or
// be listed as an explicit exemption.
//
// The list in state_sync.go is hand-maintained, and the schema is not: a
// migration adds a table and nothing connects the two. That has already gone
// wrong twice. 00028_fix_missing_tables_from_state_sync.sql exists only to
// repair nodes that state-synced while core_ern/core_mead/core_pie/core_rewards
// and friends were missing from the dump, and core_auth_cids (00038) was
// likewise missed -- consensus state, so a node that synced without it would
// disagree with the network about which cids are authorized once content-auth
// enforcement activates.
//
// The schema is derived from the migration files rather than from a live
// database so this runs in CI, where there is no postgres.
func TestSnapshotTablesCoverCoreSchema(t *testing.T) {
	schema := coreTablesFromMigrations(t)
	if len(schema) == 0 {
		t.Fatal("parsed no core_* tables out of the migrations -- this guard has stopped guarding anything")
	}

	inSnapshot := make(map[string]bool, len(stateSyncSnapshotTables))
	for _, table := range stateSyncSnapshotTables {
		if inSnapshot[table] {
			t.Errorf("%q is listed twice in stateSyncSnapshotTables", table)
		}
		inSnapshot[table] = true
	}

	var missing []string
	for _, table := range schema {
		if inSnapshot[table] {
			continue
		}
		if _, exempt := snapshotExemptCoreTables[table]; exempt {
			continue
		}
		missing = append(missing, table)
	}

	if len(missing) > 0 {
		t.Errorf("%d core_* table(s) exist in the schema but are not dumped into state-sync snapshots:\n  %s\n"+
			"A node that state-syncs comes up with these empty. If consensus reads the table, add it to "+
			"stateSyncSnapshotTables in state_sync.go; if it is node-local or rederivable, add it to "+
			"snapshotExemptCoreTables with the reason.",
			len(missing), strings.Join(missing, "\n  "))
	}

	// An exemption for a table that no longer exists is stale bookkeeping, and
	// left alone it would silently excuse a future table that reuses the name.
	for table := range snapshotExemptCoreTables {
		if !slices.Contains(schema, table) {
			t.Errorf("snapshotExemptCoreTables exempts %q, which no longer exists in the schema; drop the entry", table)
		}
	}

	// A snapshot entry naming a core_* table the schema never creates would
	// make pg_dump fail outright -- it errors on an unmatched -t pattern.
	for _, table := range stateSyncSnapshotTables {
		if !strings.HasPrefix(table, "core_") || table == "core_db_migrations" {
			// core_db_migrations is created by sql-migrate itself, not by a
			// migration file, so it is invisible to this parse. Non-core tables
			// are out of scope for this guard.
			continue
		}
		if !slices.Contains(schema, table) {
			t.Errorf("stateSyncSnapshotTables names %q, which no migration creates; pg_dump fails on an unmatched -t pattern", table)
		}
	}
}

var (
	createTableRe = regexp.MustCompile(`(?i)create\s+table\s+(?:if\s+not\s+exists\s+)?"?([a-z_0-9]+)"?`)
	dropTableRe   = regexp.MustCompile(`(?i)drop\s+table\s+(?:if\s+exists\s+)?"?([a-z_0-9]+)"?`)
)

// coreTablesFromMigrations replays the migrations' Up sections in the order
// sql-migrate applies them -- lexicographic by filename -- and returns the
// core_* tables left standing.
func coreTablesFromMigrations(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read migrations dir %s: %v", migrationsDir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	// sql-migrate sorts migrations by id, which is the file name.
	sort.Strings(names)

	alive := map[string]bool{}
	for _, name := range names {
		src, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		up := upSection(string(src))
		for _, m := range createTableRe.FindAllStringSubmatch(up, -1) {
			alive[m[1]] = true
		}
		for _, m := range dropTableRe.FindAllStringSubmatch(up, -1) {
			delete(alive, m[1])
		}
	}

	var core []string
	for table := range alive {
		if strings.HasPrefix(table, "core_") {
			core = append(core, table)
		}
	}
	sort.Strings(core)
	return core
}

// upSection returns the statements sql-migrate runs on the way up. A Down
// section drops what its Up created, so counting it would cancel out every
// table in the schema.
func upSection(src string) string {
	_, after, found := strings.Cut(src, "-- +migrate Up")
	if !found {
		return ""
	}
	before, _, _ := strings.Cut(after, "-- +migrate Down")
	return before
}
