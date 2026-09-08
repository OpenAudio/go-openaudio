package server

import (
	"strings"
	"testing"
)

// mediorumTables are created by GORM AutoMigrate (mediorum/server/db.go), not by
// core's migrations, and are never dumped into a snapshot. They share the
// `public` schema with core because both services read one OPENAUDIO_DB_URL, so
// nothing but the truncate's scope keeps state sync away from them.
//
// This is the regression: the restore used to truncate every table in `public`,
// so any node that state synced lost its upload records, its blob inventory and
// its audio analyses -- data no snapshot restores and, for uploads, data the new
// chain carries no operations to rebuild from.
var mediorumTables = []string{
	"uploads",
	"blobs",
	"audio_previews",
	"qm_audio_analyses",
	"upload_cursors",
	"repair_trackers",
	"delist_statuses",
	"delist_status_cursor",
}

func TestTruncateStmtSkipsTablesTheSnapshotDoesNotCarry(t *testing.T) {
	existing := append([]string{}, stateSyncSnapshotTables...)
	existing = append(existing, mediorumTables...)
	existing = append(existing, "etl_blocks", "etl_plays")

	stmt := truncateSnapshotTablesStmt(existing)
	if stmt == "" {
		t.Fatal("expected a TRUNCATE statement when snapshot tables exist")
	}

	for _, table := range append(append([]string{}, mediorumTables...), "etl_blocks", "etl_plays") {
		if strings.Contains(stmt, `"`+table+`"`) {
			t.Errorf("truncate statement names %q, which no snapshot restores -- truncating it only destroys data", table)
		}
	}

	// core_db_migrations is the table that motivated truncating at all: the node
	// runs migrations at startup, which populates it, and COPY then fails on
	// duplicate keys. If it stops being truncated, restores break.
	if !strings.Contains(stmt, `"core_db_migrations"`) {
		t.Error("core_db_migrations must still be truncated or pg_restore fails on duplicate keys")
	}
}

func TestTruncateStmtOnlyNamesTablesThatExist(t *testing.T) {
	// pgRestore("pre-data") tolerates schema drift, so a listed table may not be
	// present locally. Naming a missing table would fail the whole statement.
	present := stateSyncSnapshotTables[0]
	stmt := truncateSnapshotTablesStmt([]string{present, "uploads"})

	if !strings.Contains(stmt, `"`+present+`"`) {
		t.Errorf("expected %q in %q", present, stmt)
	}
	for _, table := range stateSyncSnapshotTables[1:] {
		if strings.Contains(stmt, `"`+table+`"`) {
			t.Errorf("statement names %q, which does not exist locally", table)
		}
	}
	if truncateSnapshotTablesStmt(nil) != "" {
		t.Error("expected no statement when nothing to truncate")
	}
}

func TestTruncateStmtDoesNotCascade(t *testing.T) {
	// CASCADE follows foreign keys OUTWARD, so a key from a non-snapshot table
	// into a snapshot table would silently truncate the non-snapshot table too --
	// reintroducing the bug this scoping exists to fix. Failing loudly is correct.
	stmt := truncateSnapshotTablesStmt(stateSyncSnapshotTables)
	if strings.Contains(strings.ToUpper(stmt), "CASCADE") {
		t.Error("TRUNCATE must not CASCADE: it would reach tables outside the snapshot")
	}
	if strings.Count(stmt, "TRUNCATE") != 1 {
		t.Error("expected a single TRUNCATE so foreign keys among snapshot tables are satisfied together")
	}
}
