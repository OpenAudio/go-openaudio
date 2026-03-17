// etl runs the ETL indexer against a production RPC and local Postgres.
//
// It creates all necessary tables via migrations, then indexes blocks and prints
// each transaction's payload before processing and a completion message after.
//
// Usage:
//
//	go run ./examples/etl \
//	  --rpc https://core.audius.co \
//	  --db "postgres://localhost:5432/etl_local?sslmode=disable"
//
// Environment variables ETL_RPC_URL and ETL_DB_URL are used as fallbacks.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"

	etl "github.com/OpenAudio/go-openaudio/etl"
	corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"go.uber.org/zap"
)

func main() {
	rpcURL := flag.String("rpc", "", "Core RPC endpoint (e.g. https://core.audius.co)")
	dbURL := flag.String("db", "", "Postgres connection string (e.g. postgres://localhost:5432/etl_local?sslmode=disable)")
	startBlock := flag.Int64("start", 0, "Starting block height (0 = resume from last indexed)")
	flag.Parse()

	if *rpcURL == "" {
		*rpcURL = os.Getenv("ETL_RPC_URL")
	}
	if *dbURL == "" {
		*dbURL = os.Getenv("ETL_DB_URL")
	}
	if *rpcURL == "" || *dbURL == "" {
		log.Fatal("both --rpc and --db are required (or set ETL_RPC_URL and ETL_DB_URL)")
	}

	if !strings.HasPrefix(*rpcURL, "http://") && !strings.HasPrefix(*rpcURL, "https://") {
		*rpcURL = "https://" + *rpcURL
	}

	logger, err := zap.NewDevelopment()
	if err != nil {
		log.Fatalf("failed to create logger: %v", err)
	}

	coreClient := corev1connect.NewCoreServiceClient(http.DefaultClient, *rpcURL)

	indexer := etl.New(coreClient, logger)
	indexer.SetDBURL(*dbURL)
	indexer.SetConfig(etl.Config{
		EnableMaterializedViewRefresh: false,
		EnablePgNotifyListener:        false,
	})
	if *startBlock > 0 {
		indexer.SetStartingBlockHeight(*startBlock)
	}

	logger.Info("starting ETL local runner",
		zap.String("rpc", *rpcURL),
		zap.String("db", *dbURL),
		zap.Int64("start_block", *startBlock),
	)

	if err := indexer.Run(); err != nil {
		logger.Fatal("indexer exited with error", zap.Error(err))
	}
}
