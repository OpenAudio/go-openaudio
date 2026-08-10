package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// compareOptions carries the run-level knobs. Every one of them changes what
// the run actually proves, so each is echoed in the report header.
type compareOptions struct {
	Sample       sampleConfig
	TolerancePct float64
	Aggregates   bool     // run the whole-table column checks
	Rows         bool     // run the row-by-row comparison
	ValidateOnly bool     // only check that every generated query is valid against both schemas
	Only         []string // restrict the run to these tables (empty means all)
}

func defaultCompareOptions() compareOptions {
	return compareOptions{
		Sample:       sampleConfig{Mod: sampleModAuto, Offset: 0, Target: 25000},
		TolerancePct: 0.5,
		Aggregates:   true,
		Rows:         true,
	}
}

func (o compareOptions) selected() []compareTable {
	if len(o.Only) == 0 {
		return compareTables
	}
	want := make(map[string]bool, len(o.Only))
	for _, n := range o.Only {
		want[strings.TrimSpace(n)] = true
	}
	var out []compareTable
	for _, ct := range compareTables {
		if want[ct.Name] {
			out = append(out, ct)
		}
	}
	return out
}

// prodWhere is the filter applied when looking a row up in the reference
// database.
//
// This used to default to "is_current = true" for any table that did not set
// it explicitly, which is a column three of the covered tables do not have:
// comments, muted_users and dashboard_wallet_users errored on every lookup
// with `column "is_current" does not exist`. The error was printed per table
// and then discarded, so those three contributed nothing to the summary and
// the run still exited 0. Defaulting to the table's own filter keeps the
// default honest, and query errors now fail the run.
func (ct compareTable) prodWhere() string {
	if ct.ProdWhere != "" {
		return ct.ProdWhere
	}
	if ct.Where != "" {
		return ct.Where
	}
	return "true"
}

// Compare connects to both the ETL clone and a reference database holding the
// rows this ETL run is supposed to reproduce -- either the pre-cutover rows
// written by the legacy Python indexer, or a Discovery Provider snapshot a
// genesis migration was replayed from.
//
// It checks three things, in increasing order of what they can see:
//
//   - row counts, which catch a table that did not get written at all;
//   - column aggregates, which catch a table that got the right number of rows
//     with a column empty in all of them;
//   - a row-by-row field comparison over a deterministic sample, which catches
//     values that are wrong rather than missing.
//
// The middle one is the reason this tool was extended: tracks once came out
// 1,955,896 reference rows against 1,955,877 indexed, while
// playlists_containing_track was populated on 943,784 reference rows and none
// of the indexed ones. The row counts looked fine.
func Compare(ctx context.Context, etlPool *pgxpool.Pool, prodPool *pgxpool.Pool, opts compareOptions) error {
	tables := opts.selected()
	if len(tables) == 0 {
		return fmt.Errorf("no tables selected: --tables matched none of the %d known tables", len(compareTables))
	}

	if opts.ValidateOnly {
		return validateSchemas(ctx, etlPool, prodPool, tables)
	}

	var emBlockBoundary, minGoHeight, maxGoHeight int64
	prodMaxEmBlock := int64(math.MaxInt64)
	cutoffLabel := "n/a (row comparison disabled)"

	if opts.Rows {
		// Find the em_block boundary using etl_blocks, which only the Go ETL
		// writes to. The first etl_blocks row marks where Go started indexing.
		// Everything below the corresponding em_block was written by the legacy
		// Python indexer; everything at or above was written by the Go ETL.
		err := etlPool.QueryRow(ctx,
			`SELECT MIN(block_height), MAX(block_height) FROM etl_blocks`).Scan(&minGoHeight, &maxGoHeight)
		if err != nil {
			return fmt.Errorf("no etl_blocks data — has the Go ETL run? %w", err)
		}

		// The boundary is the em_block just before the first Go-written em_block.
		err = etlPool.QueryRow(ctx,
			`SELECT COALESCE(MIN(em_block) - 1, 0)
			 FROM core_indexed_blocks
			 WHERE em_block IS NOT NULL
			   AND height >= $1`, minGoHeight).Scan(&emBlockBoundary)
		if err != nil {
			return fmt.Errorf("determining em_block boundary: %w", err)
		}

		// Find the max em_block in the reference database that corresponds to
		// the Go ETL's max chain height. Any reference entity above it was
		// modified after our comparison window and cannot be compared.
		//
		// That cutoff only means something when the reference database took
		// part in the chain, which is the Python-to-Go cutover this tool was
		// written for. Comparing a genesis replay against a restored Discovery
		// Provider snapshot is different: the snapshot has no
		// core_indexed_blocks rows at all, so MAX(em_block) is NULL, COALESCE
		// made it 0, and every row with blocknumber > 0 -- which is every row
		// -- was skipped as "ahead of the window". The run took hours and
		// compared nothing while reporting success.
		//
		// A snapshot IS the window, so there is nothing to be ahead of.
		var prodMaxEmBlockNull *int64
		err = prodPool.QueryRow(ctx,
			`SELECT MAX(em_block)
			 FROM core_indexed_blocks
			 WHERE height <= $1 AND em_block IS NOT NULL`, maxGoHeight).Scan(&prodMaxEmBlockNull)
		if err != nil {
			return fmt.Errorf("determining prod em_block cutoff: %w", err)
		}
		cutoffLabel = "unbounded (reference has no core_indexed_blocks; it is a snapshot, not a chain participant)"
		if prodMaxEmBlockNull != nil {
			prodMaxEmBlock = *prodMaxEmBlockNull
			cutoffLabel = fmt.Sprintf("%d (prod rows above this are beyond our window)", prodMaxEmBlock)
		}
	}

	fmt.Printf("=== ETL vs reference comparison ===\n")
	fmt.Printf("Tables:                %d\n", len(tables))
	if opts.Rows {
		fmt.Printf("em_block boundary:     %d (rows with blocknumber > this are Go-written)\n", emBlockBoundary)
		fmt.Printf("Go ETL chain heights:  %d .. %d\n", minGoHeight, maxGoHeight)
		fmt.Printf("Prod em_block cutoff:  %s\n", cutoffLabel)
	}
	fmt.Printf("Column aggregates:     %s\n", enabledLabel(opts.Aggregates))
	fmt.Printf("Row comparison:        %s\n", enabledLabel(opts.Rows))
	if opts.Rows {
		fmt.Printf("Sampling:              %s\n", samplingLabel(opts.Sample))
	}
	fmt.Printf("Aggregate tolerance:   %.4g%% (a check that is non-zero on one side and zero on the other always fails)\n", opts.TolerancePct)
	fmt.Println()

	var totals struct {
		compared, matched, mismatched, missing, skippedAhead, candidates int
		aggFails, aggWarns                                               int
	}
	var sampledTables []string
	var failed []string
	var tableErrs []error

	for _, ct := range tables {
		fmt.Printf("--- %s ---\n", ct.Name)

		if opts.Aggregates {
			rows, err := compareAggregates(ctx, etlPool, prodPool, ct, opts.TolerancePct)
			if err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				tableErrs = append(tableErrs, err)
			} else {
				f, w := printAggregates(rows)
				totals.aggFails += f
				totals.aggWarns += w
				if f > 0 {
					failed = append(failed, ct.Name)
				}
			}
		}

		if opts.Rows {
			r, err := compareOneTable(ctx, etlPool, prodPool, ct, emBlockBoundary, prodMaxEmBlock, opts.Sample)
			if err != nil {
				fmt.Printf("  ERROR comparing rows: %v\n", err)
				tableErrs = append(tableErrs, fmt.Errorf("%s: %w", ct.Name, err))
			} else {
				totals.compared += r.compared
				totals.matched += r.matched
				totals.mismatched += r.mismatched
				totals.missing += r.missing
				totals.skippedAhead += r.skippedAhead
				totals.candidates += r.candidates
				if r.sampleMod > 1 {
					sampledTables = append(sampledTables,
						fmt.Sprintf("%s(1/%d)", ct.Name, r.sampleMod))
				}
			}
		}
		fmt.Println()
	}

	fmt.Printf("=== Summary ===\n")
	if opts.Aggregates {
		fmt.Printf("Column aggregates: %d failed, %d within tolerance but not identical\n",
			totals.aggFails, totals.aggWarns)
		if len(failed) > 0 {
			fmt.Printf("Tables with failing aggregates: %s\n", strings.Join(failed, ", "))
		}
	}
	if opts.Rows {
		fmt.Printf("Rows compared: %d  matched: %d  mismatched: %d  missing_in_reference: %d  skipped(reference_ahead): %d\n",
			totals.compared, totals.matched, totals.mismatched, totals.missing, totals.skippedAhead)
		if totals.compared > 0 {
			fmt.Printf("Match rate: %.1f%%\n", float64(totals.matched)/float64(totals.compared)*100)
		}
		// A sampled run covered a fraction of the rows. Say so; a summary that
		// does not mention the cap reads as "covered everything".
		if len(sampledTables) > 0 {
			fmt.Printf("Sampled tables (fraction of rows compared): %s\n", strings.Join(sampledTables, ", "))
			fmt.Printf("Row-level results for those tables describe the sample only. Column aggregates above are whole-table.\n")
		} else {
			fmt.Printf("No table was sampled: every candidate row was compared.\n")
		}
	}
	fmt.Println("=== Done ===")

	// A run that proves nothing is not a pass. Report every reason it failed
	// rather than the first, so one run tells the whole story.
	var problems []error
	if len(tableErrs) > 0 {
		problems = append(problems, fmt.Errorf("%d table(s) failed to compare: %w",
			len(tableErrs), errors.Join(tableErrs...)))
	}
	if opts.Rows && totals.compared == 0 && totals.skippedAhead > 0 {
		problems = append(problems, fmt.Errorf("compared 0 rows: all %d were skipped as ahead of the "+
			"comparison window. The em_block cutoff is wrong for this pairing, "+
			"so the run proved nothing", totals.skippedAhead))
	} else if opts.Rows && totals.compared == 0 && totals.candidates > 0 {
		problems = append(problems, fmt.Errorf("compared 0 rows out of %d candidates: the run proved nothing",
			totals.candidates))
	}
	if totals.aggFails > 0 {
		problems = append(problems, fmt.Errorf("%d column aggregate check(s) failed across %d table(s)",
			totals.aggFails, len(failed)))
	}
	return errors.Join(problems...)
}

func enabledLabel(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

func samplingLabel(cfg sampleConfig) string {
	switch {
	case cfg.Mod == 1:
		return "off (every candidate row compared)"
	case cfg.Mod > 1:
		return fmt.Sprintf("fixed: mod(abs(id), %d) = %d", cfg.Mod, cfg.Offset)
	default:
		return fmt.Sprintf("auto: divisor chosen per table to yield ~%d rows, residue %d", cfg.Target, cfg.Offset)
	}
}

type compareResult struct {
	compared, matched, mismatched, missing, skippedAhead int
	candidates                                           int // rows fetched from the ETL side
	sampleMod                                            int
}

// estimateRows reads the planner's row estimate for a table. It is an estimate
// on purpose: it only picks the sample divisor, and paying for an exact
// count(*) on a 26-million-row table just to decide how much of it to read
// would defeat the point. A negative result means the table was never
// analyzed, and sampleDivisor declines to sample rather than guess.
func estimateRows(ctx context.Context, pool *pgxpool.Pool, table string) int64 {
	var est float64
	err := pool.QueryRow(ctx,
		`SELECT reltuples FROM pg_class WHERE oid = to_regclass($1)`, table).Scan(&est)
	if err != nil {
		return -1
	}
	if est < 0 {
		return -1
	}
	return int64(est)
}

func compareOneTable(ctx context.Context, etlPool, prodPool *pgxpool.Pool, ct compareTable, emBlockBoundary, prodMaxEmBlock int64, cfg sampleConfig) (compareResult, error) {
	var r compareResult

	// castCol returns the SELECT expression for a column, applying casts if configured.
	castCol := func(col string) string {
		if ct.CastCols != nil {
			if expr, ok := ct.CastCols[col]; ok {
				return expr
			}
		}
		return col
	}

	// Build column list: IDs + blocknumber (when the table has one) + compare
	// columns + known-diff columns. allCols holds bare column names for
	// indexing; selectCols holds SELECT expressions with casts.
	allCols := make([]string, 0, len(ct.IDCols)+1+len(ct.Columns)+len(ct.KnownDiffs))
	allCols = append(allCols, ct.IDCols...)
	hasBlockNumber := !ct.NoBlockNumber
	if hasBlockNumber {
		allCols = append(allCols, "blocknumber")
	}
	allCols = append(allCols, ct.Columns...)
	allCols = append(allCols, ct.KnownDiffs...)

	selectCols := make([]string, len(allCols))
	for i, col := range allCols {
		selectCols[i] = castCol(col)
	}
	colList := strings.Join(selectCols, ", ")

	idCount := len(ct.IDCols)
	bnIdx := idCount // blocknumber is right after IDs, when present
	colStartIdx := idCount
	if hasBlockNumber {
		colStartIdx++
	}
	knownDiffStartIdx := colStartIdx + len(ct.Columns)

	// Resolve the sample for this table before building the query so the
	// report can state exactly which rows were looked at.
	est := int64(-1)
	sampleMod := 1
	if ct.SampleCol != "" {
		est = estimateRows(ctx, etlPool, ct.Name)
		sampleMod = sampleDivisor(est, cfg)
	}
	r.sampleMod = sampleMod

	var preds []string
	if hasBlockNumber {
		preds = append(preds, fmt.Sprintf("blocknumber > %d", emBlockBoundary))
	}
	if ct.Where != "" {
		preds = append(preds, ct.Where)
	}
	if p := samplePredicate(ct.SampleCol, sampleMod, cfg.Offset); p != "" {
		preds = append(preds, p)
	}
	if len(preds) == 0 {
		preds = append(preds, "true")
	}

	orderBy := strings.Join(ct.IDCols, ", ")
	etlQuery := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s",
		colList, ct.Name, strings.Join(preds, " AND "), orderBy)

	etlRows, err := etlPool.Query(ctx, etlQuery)
	if err != nil {
		return r, fmt.Errorf("query etl %s: %w", ct.Name, err)
	}
	defer etlRows.Close()

	type rowData struct {
		ids        []any
		blocknum   int64
		values     map[string]any
		knownDiffs map[string]any
	}
	var etlEntities []rowData

	for etlRows.Next() {
		vals := make([]any, len(allCols))
		ptrs := make([]any, len(allCols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := etlRows.Scan(ptrs...); err != nil {
			return r, fmt.Errorf("scan etl %s: %w", ct.Name, err)
		}

		ids := make([]any, idCount)
		copy(ids, vals[:idCount])

		var bn int64
		if hasBlockNumber {
			bn = toInt64(vals[bnIdx])
		}

		m := make(map[string]any, len(ct.Columns))
		for i, col := range ct.Columns {
			m[col] = vals[colStartIdx+i]
		}

		kd := make(map[string]any, len(ct.KnownDiffs))
		for i, col := range ct.KnownDiffs {
			kd[col] = vals[knownDiffStartIdx+i]
		}

		etlEntities = append(etlEntities, rowData{ids: ids, blocknum: bn, values: m, knownDiffs: kd})
	}
	if err := etlRows.Err(); err != nil {
		return r, fmt.Errorf("read etl %s: %w", ct.Name, err)
	}
	r.candidates = len(etlEntities)

	fmt.Printf("  rows: %d candidates — %s\n", len(etlEntities), sampleNote(ct.SampleCol, sampleMod, cfg.Offset, est))
	if len(etlEntities) == 0 {
		return r, nil
	}

	// Build reference lookup query.
	var idPredicates []string
	for i, col := range ct.IDCols {
		idPredicates = append(idPredicates, fmt.Sprintf("%s = $%d", col, i+1))
	}
	prodQuery := fmt.Sprintf("SELECT %s FROM %s WHERE %s AND %s LIMIT 1",
		colList, ct.Name, strings.Join(idPredicates, " AND "), ct.prodWhere())

	var diffs []string
	var knownDiffCount int
	maxDiffsShown := 20

	for _, entity := range etlEntities {
		prodRow := prodPool.QueryRow(ctx, prodQuery, entity.ids...)
		prodVals := make([]any, len(allCols))
		prodPtrs := make([]any, len(allCols))
		for i := range prodVals {
			prodPtrs[i] = &prodVals[i]
		}

		if err := prodRow.Scan(prodPtrs...); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				r.missing++
				r.compared++
				if len(diffs) < maxDiffsShown {
					diffs = append(diffs, fmt.Sprintf("  %s: MISSING in reference", fmtIDs(ct.IDCols, entity.ids)))
				}
				continue
			}
			// Anything other than "no such row" is a broken query or a broken
			// connection, not a data difference. Failing here is what keeps a
			// schema mismatch from being reported as a clean run.
			return r, fmt.Errorf("lookup %s in reference: %w", ct.Name, err)
		}

		// Skip if the reference modified this entity after our comparison window.
		if hasBlockNumber {
			prodBN := toInt64(prodVals[bnIdx])
			if prodBN > prodMaxEmBlock {
				r.skippedAhead++
				continue
			}
		}

		r.compared++

		// Compare standard columns
		prodMap := make(map[string]any, len(ct.Columns))
		for i, col := range ct.Columns {
			prodMap[col] = prodVals[colStartIdx+i]
		}

		rowMatch := true
		var rowDiffs []string
		for _, col := range ct.Columns {
			etlVal := entity.values[col]
			prodVal := prodMap[col]
			if !valuesEqual(etlVal, prodVal) {
				rowMatch = false
				rowDiffs = append(rowDiffs, fmt.Sprintf("    %s: etl=%v reference=%v", col, fmtVal(etlVal), fmtVal(prodVal)))
			}
		}

		// Check known-diff columns (report separately, don't count as mismatch)
		for i, col := range ct.KnownDiffs {
			etlVal := entity.knownDiffs[col]
			prodVal := prodVals[knownDiffStartIdx+i]
			if !valuesEqual(etlVal, prodVal) {
				knownDiffCount++
			}
		}

		if rowMatch {
			r.matched++
		} else {
			r.mismatched++
			if len(diffs) < maxDiffsShown {
				diffs = append(diffs, fmt.Sprintf("  %s:", fmtIDs(ct.IDCols, entity.ids)))
				diffs = append(diffs, rowDiffs...)
			}
		}
	}

	fmt.Printf("  compared: %d  matched: %d  mismatched: %d  missing: %d  skipped(reference ahead): %d\n",
		r.compared, r.matched, r.mismatched, r.missing, r.skippedAhead)
	if r.compared > 0 {
		fmt.Printf("  match rate: %.1f%%\n", float64(r.matched)/float64(r.compared)*100)
	}
	if knownDiffCount > 0 {
		fmt.Printf("  known divergences (legacy indexer immutable/bug): %d rows\n", knownDiffCount)
	}
	if len(diffs) > 0 {
		fmt.Println("  differences (first 20):")
		for _, d := range diffs {
			fmt.Println(d)
		}
		if r.mismatched+r.missing > maxDiffsShown {
			fmt.Printf("  ... and %d more\n", r.mismatched+r.missing-maxDiffsShown)
		}
	}

	return r, nil
}

// validateSchemas runs every query this tool would issue against both
// databases with an impossible predicate, so a typo or a column that exists on
// only one side is caught in seconds instead of surfacing hours into a run --
// or, as happened with the is_current default, never surfacing at all.
func validateSchemas(ctx context.Context, etlPool, prodPool *pgxpool.Pool, tables []compareTable) error {
	fmt.Printf("=== Validating %d tables against both schemas ===\n", len(tables))
	var problems []error

	check := func(label, query string) {
		for _, side := range []struct {
			name string
			pool *pgxpool.Pool
		}{{"indexed", etlPool}, {"reference", prodPool}} {
			rows, err := side.pool.Query(ctx, query)
			if err == nil {
				rows.Close()
				err = rows.Err()
			}
			if err != nil {
				fmt.Printf("  FAIL %-28s [%s] %v\n", label, side.name, err)
				problems = append(problems, fmt.Errorf("%s on %s side: %w", label, side.name, err))
			}
		}
	}

	for _, ct := range tables {
		if len(ct.Aggregates) > 0 {
			check(ct.Name+" aggregates", aggregateQuery(ct)+" WHERE false")
		}

		cols := append([]string{}, ct.IDCols...)
		if !ct.NoBlockNumber {
			cols = append(cols, "blocknumber")
		}
		cols = append(cols, ct.Columns...)
		cols = append(cols, ct.KnownDiffs...)
		for i, col := range cols {
			if ct.CastCols != nil {
				if expr, ok := ct.CastCols[col]; ok {
					cols[i] = expr
				}
			}
		}
		where := ct.Where
		if where == "" {
			where = "true"
		}
		check(ct.Name+" rows", fmt.Sprintf("SELECT %s FROM %s WHERE false AND (%s)",
			strings.Join(cols, ", "), ct.Name, where))
		check(ct.Name+" reference rows", fmt.Sprintf("SELECT %s FROM %s WHERE false AND (%s)",
			strings.Join(cols, ", "), ct.Name, ct.prodWhere()))

		if ct.SampleCol != "" {
			check(ct.Name+" sample", fmt.Sprintf("SELECT 1 FROM %s WHERE false AND (%s)",
				ct.Name, samplePredicate(ct.SampleCol, 2, 0)))
		}
	}

	if len(problems) == 0 {
		fmt.Println("All queries valid against both schemas.")
		return nil
	}
	fmt.Printf("%d validation problem(s).\n", len(problems))
	return errors.Join(problems...)
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func fmtIDs(cols []string, vals []any) string {
	parts := make([]string, len(cols))
	for i, col := range cols {
		parts[i] = fmt.Sprintf("%s=%v", col, vals[i])
	}
	return strings.Join(parts, ", ")
}

// valuesEqual compares two values from pgx scans, handling nil and type differences.
func valuesEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		// Treat empty string as equivalent to nil for nullable text columns
		if a == nil {
			if s, ok := b.(string); ok && s == "" {
				return true
			}
			return false
		}
		if s, ok := a.(string); ok && s == "" {
			return true
		}
		return false
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func fmtVal(v any) string {
	if v == nil {
		return "<nil>"
	}
	s := fmt.Sprintf("%v", v)
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}
