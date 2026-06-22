// etl runs the ETL indexer against an RPC endpoint and a local Postgres.
//
// It creates all necessary tables via migrations, then indexes blocks and prints
// each transaction's payload before processing and a completion message after.
//
// Against production (valid TLS, polling):
//
//	go run ./examples/etl \
//	  --rpc https://core.audius.co \
//	  --db "postgres://localhost:5432/etl_local?sslmode=disable"
//
// Against the local devnet over the gRPC block stream (node1's h2c port):
//
//	go run ./examples/etl \
//	  --rpc http://localhost:50051 \
//	  --db "postgres://etl:etl@localhost:5454/etl?sslmode=disable" \
//	  --stream
//
// Or exercise the nginx 443 path (self-signed TLS) with --insecure:
//
//	go run ./examples/etl --rpc https://node1.oap.devnet --db ... --stream --insecure
//
// Environment variables ETL_RPC_URL and ETL_DB_URL are used as fallbacks.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"connectrpc.com/connect"
	corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	etl "github.com/OpenAudio/go-openaudio/pkg/etl"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
)

func main() {
	rpcURL := flag.String("rpc", "", "Core RPC endpoint (e.g. https://core.audius.co, http://localhost:50051)")
	dbURL := flag.String("db", "", "Postgres connection string (e.g. postgres://localhost:5432/etl_local?sslmode=disable)")
	startBlock := flag.Int64("start", 0, "Starting block height (0 = resume from last indexed)")
	endBlock := flag.Int64("end", 0, "Ending block height (0 = run forever)")
	skipMigrations := flag.Bool("skip-migrations", false, "Skip running database migrations (use with pre-existing schemas)")
	stream := flag.Bool("stream", false, "Consume blocks via the gRPC StreamBlocks stream instead of polling GetBlocks")
	insecure := flag.Bool("insecure", false, "Skip TLS verification (for the self-signed devnet cert over https)")
	verbose := flag.Bool("v", false, "Enable debug logging")
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

	cfg := zap.NewDevelopmentConfig()
	cfg.DisableStacktrace = true
	if !*verbose {
		cfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}
	logger, err := cfg.Build()
	if err != nil {
		log.Fatalf("failed to create logger: %v", err)
	}

	// The block stream is a gRPC server stream, so the client must speak HTTP/2.
	// For http:// (e.g. node1's :50051) that's h2c; for https:// it's TLS h2.
	httpClient := http.DefaultClient
	var clientOpts []connect.ClientOption
	// http:// endpoints (e.g. node1's :50051) are h2c-only; --insecure/--stream
	// also need an HTTP/2-capable client. Plain https stays on the default client.
	if *stream || *insecure || strings.HasPrefix(*rpcURL, "http://") {
		httpClient = buildH2Client(*rpcURL, *insecure)
	}
	if *stream {
		clientOpts = append(clientOpts, connect.WithGRPC())
	}

	coreClient := corev1connect.NewCoreServiceClient(httpClient, *rpcURL, clientOpts...)

	indexer := etl.New(coreClient, logger)
	indexer.SetDBURL(*dbURL)
	etlCfg := etl.Config{
		EnableMaterializedViewRefresh: false,
		EnablePgNotifyListener:        false,
	}
	if *stream {
		etlCfg.BlockStreamEnabled = true
		indexer.SetBlockStreamClient(coreClient)
	}
	indexer.SetConfig(etlCfg)
	if *startBlock > 0 {
		indexer.SetStartingBlockHeight(*startBlock)
	}
	if *endBlock > 0 {
		indexer.SetEndingBlockHeight(*endBlock)
	}
	if *skipMigrations {
		indexer.SetSkipMigrations(true)
	}

	logger.Info("starting ETL local runner",
		zap.String("rpc", *rpcURL),
		zap.String("db", *dbURL),
		zap.Bool("stream", *stream),
		zap.Int64("start_block", *startBlock),
	)

	if err := indexer.Run(); err != nil {
		logger.Fatal("indexer exited with error", zap.Error(err))
	}
}

// buildH2Client returns an HTTP/2-capable client: h2c (cleartext) for http://
// endpoints like node1's :50051, or TLS h2 (optionally skipping verification of
// the self-signed devnet cert) for https://.
func buildH2Client(rpcURL string, insecure bool) *http.Client {
	if strings.HasPrefix(rpcURL, "http://") {
		return &http.Client{
			Transport: &http2.Transport{
				AllowHTTP: true,
				DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, addr)
				},
			},
		}
	}
	tr := &http.Transport{ForceAttemptHTTP2: true}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &http.Client{Transport: tr}
}
