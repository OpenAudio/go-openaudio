// Parity compare tool: field-by-field comparison of Go ETL output against the
// pre-cutover rows written by the legacy Python indexer.
//
// This tool was used to validate the production cutover from the legacy Python
// celery indexer to the Go ETL (now complete — the Go ETL is the production
// indexer). It remains useful for auditing the Go ETL against a snapshot of
// historical data.
//
// Usage:
//
//	go run ./pkg/etl/parity --db "$ETL_DB_URL" --prod-db "$PROD_DB_URL"
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dbURL := flag.String("db", os.Getenv("ETL_DB_URL"), "Postgres connection string (ETL clone)")
	prodURL := flag.String("prod-db", os.Getenv("PROD_DB_URL"), "Production Postgres connection string")
	flag.Parse()

	if *dbURL == "" {
		log.Fatal("--db is required (or set ETL_DB_URL)")
	}
	if *prodURL == "" {
		log.Fatal("--prod-db is required (or set PROD_DB_URL)")
	}

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, *dbURL)
	if err != nil {
		log.Fatalf("connect to ETL db: %v", err)
	}
	defer pool.Close()

	prodPool, err := pgxpool.New(ctx, *prodURL)
	if err != nil {
		log.Fatalf("connect to prod db: %v", err)
	}
	defer prodPool.Close()

	if err := Compare(ctx, pool, prodPool); err != nil {
		log.Fatalf("compare failed: %v", err)
	}
}
