package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// aggStatus is the verdict for a single aggregate.
type aggStatus int

const (
	aggOK aggStatus = iota
	aggWarn
	aggFail
)

func (s aggStatus) String() string {
	switch s {
	case aggOK:
		return "OK"
	case aggWarn:
		return "WARN"
	default:
		return "FAIL"
	}
}

// aggRow is one aggregate evaluated on both sides.
type aggRow struct {
	Name    string
	Source  int64 // reference database (the snapshot being migrated from)
	Indexed int64 // ETL output
	Status  aggStatus
	Note    string
}

// classifyAgg decides how bad a difference between the two sides is.
//
// Two rules, and the first one is the whole point of this file:
//
//  1. If one side has rows and the other has none, that is always a failure,
//     no matter how small the column is or how generous the tolerance. An
//     all-or-nothing difference is a column that did not get indexed, not
//     drift. 943,784 against 0 is what this catches.
//
//  2. Otherwise the difference is measured against the reference side and
//     compared to the tolerance. Within tolerance is a warning, not a pass:
//     nothing here is expected to differ, and the reason the original bug
//     survived is that a small plausible-looking delta reads as noise.
func classifyAgg(source, indexed int64, tolerancePct float64) (aggStatus, string) {
	if source == indexed {
		return aggOK, ""
	}
	if source > 0 && indexed == 0 {
		return aggFail, "empty on the indexed side"
	}
	if indexed > 0 && source == 0 {
		return aggFail, "absent from the reference side"
	}
	diff := source - indexed
	if diff < 0 {
		diff = -diff
	}
	denom := source
	if denom < 0 {
		denom = -denom
	}
	if denom == 0 {
		denom = 1
	}
	pct := float64(diff) / float64(denom) * 100
	if pct <= tolerancePct {
		return aggWarn, fmt.Sprintf("%.4f%% off", pct)
	}
	return aggFail, fmt.Sprintf("%.4f%% off", pct)
}

// aggregateQuery builds the single-scan query that evaluates every check for a
// table. count(*) is always the first column so that the row count is reported
// next to the column-level checks it cannot substitute for.
func aggregateQuery(ct compareTable) string {
	exprs := make([]string, 0, len(ct.Aggregates)+1)
	exprs = append(exprs, "count(*)::bigint")
	for _, a := range ct.Aggregates {
		exprs = append(exprs, "("+a.Expr+")::bigint")
	}
	return "SELECT " + strings.Join(exprs, ", ") + " FROM " + ct.Name
}

// aggregateNames are the report labels matching aggregateQuery's columns.
func aggregateNames(ct compareTable) []string {
	names := make([]string, 0, len(ct.Aggregates)+1)
	names = append(names, "row_count")
	for _, a := range ct.Aggregates {
		names = append(names, a.Name)
	}
	return names
}

func scanAggregates(ctx context.Context, pool *pgxpool.Pool, query string, n int) ([]int64, error) {
	vals := make([]int64, n)
	ptrs := make([]any, n)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := pool.QueryRow(ctx, query).Scan(ptrs...); err != nil {
		return nil, err
	}
	return vals, nil
}

// compareAggregates evaluates every column-level check for one table on both
// databases and reports the differences.
func compareAggregates(ctx context.Context, etlPool, prodPool *pgxpool.Pool, ct compareTable, tolerancePct float64) ([]aggRow, error) {
	if len(ct.Aggregates) == 0 {
		return nil, nil
	}
	query := aggregateQuery(ct)
	names := aggregateNames(ct)

	srcVals, err := scanAggregates(ctx, prodPool, query, len(names))
	if err != nil {
		return nil, fmt.Errorf("reference aggregates for %s: %w", ct.Name, err)
	}
	etlVals, err := scanAggregates(ctx, etlPool, query, len(names))
	if err != nil {
		return nil, fmt.Errorf("indexed aggregates for %s: %w", ct.Name, err)
	}

	rows := make([]aggRow, len(names))
	for i, name := range names {
		status, note := classifyAgg(srcVals[i], etlVals[i], tolerancePct)
		rows[i] = aggRow{Name: name, Source: srcVals[i], Indexed: etlVals[i], Status: status, Note: note}
	}
	return rows, nil
}

// printAggregates renders the aggregate report for one table and returns the
// number of failures and warnings.
func printAggregates(rows []aggRow) (fails, warns int) {
	if len(rows) == 0 {
		return 0, 0
	}
	fmt.Printf("  column aggregates (whole table, reference vs indexed):\n")
	fmt.Printf("    %-34s %14s %14s %14s  %s\n", "check", "reference", "indexed", "delta", "status")
	for _, r := range rows {
		switch r.Status {
		case aggFail:
			fails++
		case aggWarn:
			warns++
		}
		status := r.Status.String()
		if r.Note != "" {
			status += " (" + r.Note + ")"
		}
		fmt.Printf("    %-34s %14d %14d %14d  %s\n", r.Name, r.Source, r.Indexed, r.Indexed-r.Source, status)
	}
	if fails == 0 && warns == 0 {
		fmt.Printf("    all %d checks identical on both sides\n", len(rows))
	}
	return fails, warns
}
