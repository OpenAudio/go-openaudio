package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
)

// Earlier restores set every table in `public` to UNLOGGED for the data load
// and back to LOGGED afterwards, logging and ignoring any table that failed to
// convert back. Two tables reliably failed: the LOGGED pass ran in alphabetical
// order, and sla_node_reports (which references sla_rollups) sorts first, so
// postgres refused it. A table could also fail for lack of disk, since SET
// LOGGED rewrites the whole table.
//
// An UNLOGGED table is emptied by crash recovery, and the container entrypoint
// does not shut postgres down cleanly, so those tables lost their contents on
// every container stop. sla_node_reports feeds SLA rollup validation, so a node
// that lost it disagreed with the network about every rollup.
//
// Restores no longer change table persistence. What remains is repairing nodes
// that earlier restores left in that state.

// autoRepairUnloggedMaxBytes bounds the tables startup converts back to LOGGED
// on its own. SET LOGGED rewrites the table and writes all of it to WAL, so a
// large table needs roughly twice its size free; those are reported instead.
const autoRepairUnloggedMaxBytes int64 = 1 << 30 // 1 GiB

type unloggedTable struct {
	Name  string
	Bytes int64
}

type pgExecQuerier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
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

// setTablesLogged converts tables to LOGGED. Postgres refuses to make a table
// LOGGED while it references an UNLOGGED one, so tables that fail are retried
// after the rest until a pass converts nothing; whatever is left is returned
// as an error.
func setTablesLogged(ctx context.Context, q pgExecQuerier, tables []string) error {
	remaining := append([]string(nil), tables...)
	failures := map[string]error{}
	for len(remaining) > 0 {
		var next []string
		for _, table := range remaining {
			if _, err := q.Exec(ctx, "ALTER TABLE "+pgx.Identifier{table}.Sanitize()+" SET LOGGED"); err != nil {
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
	if len(failures) == 0 {
		return nil
	}

	msgs := make([]string, 0, len(failures))
	for _, table := range remaining {
		msgs = append(msgs, fmt.Sprintf("%s: %v", table, failures[table]))
	}
	return fmt.Errorf("could not set tables LOGGED: %s", strings.Join(msgs, "; "))
}

// ensureSnapshotTablesLogged converts any UNLOGGED snapshot table to LOGGED.
// The restore runs it after truncating, when the tables are empty and the
// conversion costs nothing, so data loaded into them is crash-safe.
func ensureSnapshotTablesLogged(ctx context.Context, q pgExecQuerier) error {
	unlogged, err := unloggedPublicTables(ctx, q)
	if err != nil {
		return err
	}

	inSnapshot := make(map[string]bool, len(stateSyncSnapshotTables))
	for _, t := range stateSyncSnapshotTables {
		inSnapshot[t] = true
	}
	var tables []string
	for _, t := range unlogged {
		if inSnapshot[t.Name] {
			tables = append(tables, t.Name)
		}
	}
	return setTablesLogged(ctx, q, tables)
}

// repairUnloggedTables runs once at startup, before the CometBFT node starts,
// so nothing is writing to these tables yet. It converts small UNLOGGED tables
// back to LOGGED and reports the rest with the command to fix them by hand.
// It never fails startup.
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
