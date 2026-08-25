package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Columns that describe where a row came from rather than what it holds. They
// are compared structurally (blocknumber drives the boundary filter) or are
// meaningless across two independently written databases.
var coverageIgnored = map[string]bool{
	"blocknumber": true, "blockhash": true, "txhash": true, "slot": true,
	"is_current": true, "created_at": true, "updated_at": true,
}

// A column that exists in BOTH schemas and is named nowhere in tables.go is
// invisible to every check this tool runs: row counts do not move, aggregates
// do not move, and the row comparison never selects it. It is therefore free to
// regress silently.
//
// The gap this guards is specifically the one a migration opens. A column
// present only in the reference is legitimately uncomparable, and gets recorded
// as such. When a later migration adds it to the ETL schema, that classification
// silently becomes wrong and nothing re-derives it -- which is exactly what
// happened to email_access.is_initial after it landed, leaving the fix that
// added it unverified.
func TestEveryComparableColumnIsAccountedFor(t *testing.T) {
	etlURL, prodURL := os.Getenv("ETL_DB_URL"), os.Getenv("PROD_DB_URL")
	if etlURL == "" || prodURL == "" {
		t.Skip("ETL_DB_URL and PROD_DB_URL not set, skipping schema coverage check")
	}
	ctx := context.Background()

	etl, err := pgxpool.New(ctx, etlURL)
	if err != nil {
		t.Fatalf("connect etl: %v", err)
	}
	defer etl.Close()
	prod, err := pgxpool.New(ctx, prodURL)
	if err != nil {
		t.Fatalf("connect reference: %v", err)
	}
	defer prod.Close()

	columns := func(p *pgxpool.Pool, table string) (map[string]bool, error) {
		rows, err := p.Query(ctx, `SELECT column_name FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1`, table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				return nil, err
			}
			out[c] = true
		}
		return out, rows.Err()
	}

	var unaccounted []string
	for _, ct := range compareTables {
		named := map[string]bool{}
		for _, c := range ct.IDCols {
			named[c] = true
		}
		for _, c := range ct.Columns {
			named[c] = true
		}
		for _, c := range ct.KnownDiffs {
			named[c] = true
		}

		etlCols, err := columns(etl, ct.Name)
		if err != nil {
			t.Fatalf("read etl columns for %s: %v", ct.Name, err)
		}
		prodCols, err := columns(prod, ct.Name)
		if err != nil {
			t.Fatalf("read reference columns for %s: %v", ct.Name, err)
		}

		for c := range prodCols {
			if coverageIgnored[c] || named[c] || !etlCols[c] {
				continue
			}
			unaccounted = append(unaccounted, fmt.Sprintf("%s.%s", ct.Name, c))
		}
	}

	if len(unaccounted) > 0 {
		sort.Strings(unaccounted)
		t.Errorf("%d column(s) exist in both schemas but are named nowhere in tables.go, so nothing "+
			"would notice them regressing. Add each to Columns if the migration carries it, or to "+
			"KnownDiffs if it deliberately does not:\n  %s",
			len(unaccounted), strings.Join(unaccounted, "\n  "))
	}
}
