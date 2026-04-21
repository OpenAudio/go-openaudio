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
