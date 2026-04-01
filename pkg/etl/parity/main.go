// Parity test tool for comparing Go ETL output against existing discovery-provider data.
//
// Usage:
//
//	go run ./pkg/etl/parity snapshot --db "postgres://..."
//	go run ./pkg/etl/parity diff --db "postgres://..."
//	go run ./pkg/etl/parity cleanup --db "postgres://..."
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: go run ./pkg/etl/parity <snapshot|diff|cleanup> --db <postgres-url>\n")
		os.Exit(1)
	}

	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dbURL := fs.String("db", os.Getenv("ETL_DB_URL"), "Postgres connection string")
	fs.Parse(os.Args[2:])

	if *dbURL == "" {
		log.Fatal("--db is required (or set ETL_DB_URL)")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dbURL)
	if err != nil {
		log.Fatalf("connect to db: %v", err)
	}
	defer pool.Close()

	switch cmd {
	case "snapshot":
		if err := Snapshot(ctx, pool); err != nil {
			log.Fatalf("snapshot failed: %v", err)
		}
	case "diff":
		if err := Diff(ctx, pool); err != nil {
			log.Fatalf("diff failed: %v", err)
		}
	case "cleanup":
		if err := Cleanup(ctx, pool); err != nil {
			log.Fatalf("cleanup failed: %v", err)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\nUsage: go run ./pkg/etl/parity <snapshot|diff|cleanup> --db <url>\n", cmd)
		os.Exit(1)
	}
}

// Cleanup drops the _parity_meta table.
func Cleanup(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `DROP TABLE IF EXISTS _parity_meta`)
	if err != nil {
		return err
	}
	fmt.Println("Cleaned up _parity_meta table.")
	return nil
}
