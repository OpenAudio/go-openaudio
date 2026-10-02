package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
)

// Restores load snapshot data into UNLOGGED tables so the load writes no WAL,
// which is what stopped restores of large tables from being OOM-killed (#253),
// and convert them back to LOGGED before indexes are built.
//
// That conversion used to log and ignore any table that failed, and two kinds
// reliably did:
//
//   - The LOGGED pass ran alphabetically. sla_node_reports references
//     sla_rollups and sorts first, and postgres will not make a table LOGGED
//     while it references an UNLOGGED one, so every restore left
//     sla_node_reports UNLOGGED.
//   - SET LOGGED rewrites the table and writes all of it to WAL, so it needs
//     about twice the table's size free. A large table on a full disk stayed
//     UNLOGGED.
//
// Crash recovery empties UNLOGGED tables, and the container entrypoint does not
// shut postgres down cleanly, so those tables lost their contents on every
// container stop. sla_node_reports feeds SLA rollup validation.
//
// The conversion also used to cover every table in `public`, rewriting
// mediorum's tables twice per restore for no benefit. It now covers only the
// tables the snapshot loads, retries tables whose references are not converted
// yet, checks disk before converting back, and fails the restore if any table
// stays UNLOGGED.

// loggedHeadroomFactor is the free space SET LOGGED needs, as a multiple of
// the table: a new copy of the table plus the WAL for it.
const loggedHeadroomFactor = 2

// autoRepairUnloggedMaxBytes bounds the tables startup converts back to LOGGED
// on its own; larger ones are reported instead, since they need about twice
// their size free.
const autoRepairUnloggedMaxBytes int64 = 1 << 30 // 1 GiB

type unloggedTable struct {
	Name  string
	Bytes int64
}

type pgExecQuerier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// unloggedPublicTables lists the UNLOGGED tables in `public` with their total
// size, largest first.
func unloggedPublicTables(ctx context.Context, q pgExecQuerier) ([]unloggedTable, error) {
	rows, err := q.Query(ctx, `
		SELECT c.relname, pg_total_relation_size(c.oid)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relpersistence = 'u'
		ORDER BY pg_total_relation_size(c.oid) DESC, c.relname`)
	if err != nil {
		return nil, fmt.Errorf("list unlogged tables: %w", err)
	}
	defer rows.Close()

	var tables []unloggedTable
	for rows.Next() {
		var t unloggedTable
		if err := rows.Scan(&t.Name, &t.Bytes); err != nil {
			return nil, err
		}
		tables = append(tables, t)
	}
	return tables, rows.Err()
}

// existingSnapshotTables returns the snapshot tables present in `public`.
func existingSnapshotTables(ctx context.Context, q pgExecQuerier) ([]string, error) {
	rows, err := q.Query(ctx, "SELECT tablename FROM pg_tables WHERE schemaname = 'public'")
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()

	existing := map[string]bool{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		existing[t] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var tables []string
	for _, t := range stateSyncSnapshotTables {
		if existing[t] {
			tables = append(tables, t)
		}
	}
	return tables, nil
}

// setTablesPersistence sets each table to persistence ("LOGGED" or
// "UNLOGGED"). Postgres refuses to make a table LOGGED while it references an
// UNLOGGED one, and to make a table UNLOGGED while a LOGGED one references it,
// so tables that fail are retried after the rest until a pass converts nothing.
// It returns the tables that could not be converted and why.
func setTablesPersistence(ctx context.Context, q pgExecQuerier, tables []string, persistence string) map[string]error {
	remaining := append([]string(nil), tables...)
	failures := map[string]error{}
	for len(remaining) > 0 {
		var next []string
		for _, table := range remaining {
			if _, err := q.Exec(ctx, "ALTER TABLE "+pgx.Identifier{table}.Sanitize()+" SET "+persistence); err != nil {
				failures[table] = err
				next = append(next, table)
				continue
			}
			delete(failures, table)
		}
		if len(next) == len(remaining) {
			break
		}
		remaining = next
	}
	return failures
}

func persistenceError(persistence string, failures map[string]error) error {
	if len(failures) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(failures))
	for table, err := range failures {
		msgs = append(msgs, fmt.Sprintf("%s: %v", table, err))
	}
	return fmt.Errorf("could not set tables %s: %s", persistence, strings.Join(msgs, "; "))
}

// setTablesLogged converts tables to LOGGED, in whatever order postgres
// accepts, and returns an error naming any it could not convert.
func setTablesLogged(ctx context.Context, q pgExecQuerier, tables []string) error {
	return persistenceError("LOGGED", setTablesPersistence(ctx, q, tables, "LOGGED"))
}

// checkLoggedHeadroom fails when there is not enough free space to convert the
// largest table back to LOGGED.
func checkLoggedHeadroom(largest unloggedTable, freeBytes int64) error {
	need := largest.Bytes * loggedHeadroomFactor
	if freeBytes >= need {
		return nil
	}
	return fmt.Errorf("not enough free disk to make %s LOGGED again: it is %d MiB, which needs about %d MiB free, and %d MiB is free",
		largest.Name, largest.Bytes>>20, need>>20, freeBytes>>20)
}

// postgresDataDirFreeBytes returns the free space on postgres's data
// directory. It only works when postgres runs on this machine, as it does in
// the openaudio container.
func postgresDataDirFreeBytes(ctx context.Context, q pgExecQuerier) (int64, error) {
	var dataDir string
	if err := q.QueryRow(ctx, "SHOW data_directory").Scan(&dataDir); err != nil {
		return 0, fmt.Errorf("read data_directory: %w", err)
	}
	return snapshotDirFreeBytes(dataDir)
}

// restoreSnapshotTablesLogged converts the snapshot tables back to LOGGED after
// the data load. It checks disk first, so a full disk fails the restore with a
// clear message instead of leaving tables UNLOGGED, and fails if any table
// cannot be converted.
func restoreSnapshotTablesLogged(ctx context.Context, q pgExecQuerier, logger *zap.Logger) error {
	unlogged, err := unloggedPublicTables(ctx, q)
	if err != nil {
		return err
	}

	inSnapshot := make(map[string]bool, len(stateSyncSnapshotTables))
	for _, t := range stateSyncSnapshotTables {
		inSnapshot[t] = true
	}
	var tables []string
	var largest unloggedTable
	for _, t := range unlogged {
		if !inSnapshot[t.Name] {
			continue
		}
		tables = append(tables, t.Name)
		if t.Bytes > largest.Bytes {
			largest = t
		}
	}
	if len(tables) == 0 {
		return nil
	}

	// Tables convert one at a time and each frees its old copy when it
	// commits, so the largest table sets the requirement.
	if freeBytes, err := postgresDataDirFreeBytes(ctx, q); err != nil {
		logger.Warn("could not check free disk before making tables LOGGED; converting anyway", zap.Error(err))
	} else if err := checkLoggedHeadroom(largest, freeBytes); err != nil {
		return err
	}

	return setTablesLogged(ctx, q, tables)
}

// repairUnloggedTables runs once at startup, before the CometBFT node starts,
// so nothing is writing to these tables yet. It converts small UNLOGGED tables
// in `public` back to LOGGED, mediorum's included since earlier restores
// converted every table, and reports the rest with the command to fix them by
// hand. It never fails startup.
func (s *Server) repairUnloggedTables(ctx context.Context) {
	if s.pool == nil {
		return
	}

	unlogged, err := unloggedPublicTables(ctx, s.pool)
	if err != nil {
		s.logger.Warn("could not check for unlogged tables", zap.Error(err))
		return
	}
	if len(unlogged) == 0 {
		return
	}

	var small, large []string
	for _, t := range unlogged {
		if t.Bytes <= autoRepairUnloggedMaxBytes {
			small = append(small, t.Name)
		} else {
			large = append(large, fmt.Sprintf("%s (%d MiB)", t.Name, t.Bytes>>20))
		}
	}

	if len(small) > 0 {
		if err := setTablesLogged(ctx, s.pool, small); err != nil {
			s.logger.Error("tables left UNLOGGED by an earlier state sync restore could not be repaired; "+
				"they are emptied whenever postgres stops uncleanly",
				zap.Strings("tables", small), zap.Error(err))
		} else {
			s.logger.Info("repaired tables left UNLOGGED by an earlier state sync restore", zap.Strings("tables", small))
		}
	}

	if len(large) > 0 {
		s.logger.Error("tables are UNLOGGED, probably left by an earlier state sync restore, and are emptied whenever "+
			"postgres stops uncleanly; they are too large to convert automatically. Run ALTER TABLE <table> SET LOGGED "+
			"for each, with about twice the table's size free on the database disk",
			zap.Strings("tables", large))
	}
}
