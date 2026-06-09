# ETL Local Runner

Runs the ETL indexer against a production RPC endpoint and local Postgres.
Migrations run automatically on startup.

## Quickstart

Start a local Postgres:

```bash
docker run -d --name etl-postgres \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=etl_local \
  -p 5432:5432 \
  postgres:16
```

Run the indexer:

```bash
go run ./examples/etl \
  --rpc https://rpc.audius.co \
  --db "postgres://postgres:postgres@localhost:5432/etl_local?sslmode=disable" \
  --start 21343648
```

## Flags

| Flag | Env Var | Description |
|------|---------|-------------|
| `--rpc` | `ETL_RPC_URL` | Core RPC endpoint |
| `--db` | `ETL_DB_URL` | Postgres connection string |
| `--start` | — | Starting block height (0 = resume from last indexed) |
| `--end` | — | Ending block height (0 = run forever) |
| `--stream` | — | Consume blocks via the gRPC `StreamBlocks` stream instead of polling |
| `--insecure` | — | Skip TLS verification (self-signed devnet cert over https) |
| `--skip-migrations` | — | Skip running migrations (pre-existing schema) |

## Local devnet harness

Exercise the indexer end to end against the local devnet — stream vs poll, feed
transactions, kill/restart for resume. Convenience `make` targets:

```bash
make up           # 4-node devnet (node1 exposes gRPC h2c on :50051)
make etl-db-up    # throwaway Postgres on localhost:5454

make etl-stream   # index via gRPC StreamBlocks   (node1 :50051)
make etl-poll     # index via GetBlocks polling    (for comparison)

make etl-load     # fire a batch of entity-manager txs at node1
```

Kill the ETL (`Ctrl-C`) and re-run `make etl-stream`: it resumes from its
`etl_blocks` cursor with no gap/dup (catch-up + resume under test). Do it while
`make etl-load` runs to confirm blocks committed during downtime are backfilled
on reconnect.

To test the prod-like nginx 443 path instead of the direct h2c port:

```bash
go run ./examples/etl --rpc https://node1.oap.devnet --db "$ETL_DB_URL" --stream --insecure
```

See `examples/loadgen` for the transaction generator and its flags
(`--users`, `--tracks`, `--social`). Tear down with `make etl-db-down` and `make down`.
