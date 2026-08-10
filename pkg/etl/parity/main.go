// Parity compare tool: checks Go ETL output against a reference database that
// holds the rows it is supposed to reproduce -- the pre-cutover rows written by
// the legacy Python indexer, or a Discovery Provider snapshot that a genesis
// migration was replayed from.
//
// It runs three kinds of check, in increasing order of what they can see:
//
//   - row counts, which catch a table that did not get written at all;
//   - whole-table column aggregates, which catch a table that got the right
//     number of rows with a column empty in all of them;
//   - a field-by-field comparison over a deterministic sample of rows, which
//     catches values that are wrong rather than missing.
//
// Usage:
//
//	go run ./pkg/etl/parity --db "$ETL_DB_URL" --prod-db "$PROD_DB_URL"
//
// Check the queries against both schemas before committing to a long run:
//
//	go run ./pkg/etl/parity --db ... --prod-db ... --validate-only
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	defaults := defaultCompareOptions()

	dbURL := flag.String("db", os.Getenv("ETL_DB_URL"), "Postgres connection string (ETL clone)")
	prodURL := flag.String("prod-db", os.Getenv("PROD_DB_URL"), "Reference Postgres connection string")
	plays := flag.Bool("plays", false, "Also compare plays (row counts, full aggregate_plays, and a bounded sample)")
	playsSample := flag.Int("plays-sample", 2000, "Number of plays to compare row by row when --plays is set (0 to skip)")

	aggregates := flag.Bool("aggregates", defaults.Aggregates, "Run whole-table column aggregate checks")
	rows := flag.Bool("rows", defaults.Rows, "Run the row-by-row field comparison")
	tolerance := flag.Float64("tolerance-pct", defaults.TolerancePct,
		"Aggregate difference tolerated before a check fails. A check that is non-zero on one side and zero on the other fails regardless")
	sampleMod := flag.Int("sample-mod", defaults.Sample.Mod,
		"Row sample divisor N, selecting rows where mod(abs(id), N) = offset. 1 compares every row; 0 picks N per table from its size")
	sampleOffset := flag.Int("sample-offset", defaults.Sample.Offset, "Residue kept by the row sample, in [0, sample-mod)")
	sampleTarget := flag.Int("sample-rows", defaults.Sample.Target, "Target number of sampled rows per table when --sample-mod=0")
	only := flag.String("tables", "", "Comma-separated table names to restrict the run to (default: all)")
	validateOnly := flag.Bool("validate-only", false, "Only check that every generated query is valid against both schemas, then exit")
	flag.Parse()

	if *dbURL == "" {
		log.Fatal("--db is required (or set ETL_DB_URL)")
	}
	if *prodURL == "" {
		log.Fatal("--prod-db is required (or set PROD_DB_URL)")
	}
	if !*aggregates && !*rows && !*validateOnly {
		log.Fatal("nothing to do: --aggregates and --rows are both false")
	}
	if *sampleMod < 0 {
		log.Fatal("--sample-mod must be 0 (auto) or a positive divisor")
	}
	if *sampleMod > 1 && (*sampleOffset < 0 || *sampleOffset >= *sampleMod) {
		log.Fatalf("--sample-offset must be in [0, %d)", *sampleMod)
	}

	opts := compareOptions{
		Sample: sampleConfig{
			Mod:    *sampleMod,
			Offset: *sampleOffset,
			Target: *sampleTarget,
		},
		TolerancePct: *tolerance,
		Aggregates:   *aggregates,
		Rows:         *rows,
		ValidateOnly: *validateOnly,
		Only:         splitTables(*only),
	}

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, *dbURL)
	if err != nil {
		log.Fatalf("connect to ETL db: %v", err)
	}
	defer pool.Close()

	prodPool, err := pgxpool.New(ctx, *prodURL)
	if err != nil {
		log.Fatalf("connect to reference db: %v", err)
	}
	defer prodPool.Close()

	if err := Compare(ctx, pool, prodPool, opts); err != nil {
		log.Fatalf("compare failed: %v", err)
	}

	if *plays && !*validateOnly {
		if err := ComparePlays(ctx, pool, prodPool, *playsSample); err != nil {
			log.Fatalf("compare plays failed: %v", err)
		}
	}
}

// splitTables parses the --tables list, ignoring empty entries.
func splitTables(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
