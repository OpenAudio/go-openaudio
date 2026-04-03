# genesis-writer

Populates a new Core chain with full historical Audius state by writing
synthetic blocks **directly to PostgreSQL**, without going through consensus.
After writing, it primes CometBFT's `state.db` and `blockstore.db` so a
single node can start from the written height and immediately propose the
next live block.

This is the offline alternative to `genesis-replay`. Where `genesis-replay`
requires a running network and submits every entity through `ForwardTransaction`,
`genesis-writer` works entirely against a database: no network is needed during
the main migration work.

## How it works

1. **Read** — streams every current, non-deleted entity from a source
   Discovery Provider PostgreSQL database, ordered by entity type (users →
   tracks → playlists → social → plays) to satisfy indexer dependencies.

2. **Sign** — wraps each entity in a `ManageEntityLegacyMigration` proto and signs it
   with the genesis migration keypair using EIP-712 (same structure as `ManageEntityLegacy`,
   same keypair and domain as `genesis-replay`). The distinct proto type signals to indexers
   that only the genesis migration authority needs to be verified — ownership, wallet-uniqueness,
   and handle checks must not be applied.

3. **Pack** — accumulates signed transactions into real CometBFT blocks
   (via `MakeBlock`) up to `--max-txs-per-block`, signed with the
   validator's ed25519 key.

4. **Write** — inserts into `core_blocks`, `core_transactions`, and
   `core_app_state` in a single PostgreSQL transaction per block.

5. **Prime** — writes CometBFT `state.db` (via `Bootstrap`) and
   `blockstore.db` (via `SaveSeenCommit` signed with the validator's ed25519
   key) so the genesis node can start from height N and propose N+1. Also
   updates `genesis.json` with the migration address and end height.

## Quick start

The simplest invocation only needs a source database and a data directory.
Everything else is auto-generated:

```bash
genesis-writer write \
  --src-dsn "postgres://user@host:5432/audius_dp?sslmode=disable" \
  --data-dir /data/genesis-output \
  --chain-id audius-mainnet-beta \
  --network prod
```

This will:
- Start a managed PostgreSQL instance at `<data-dir>/postgres/`
- Run Core chain schema migrations automatically
- Generate a migration Ethereum keypair (saved for resume, deleted on success)
- Generate `genesis.json` and `priv_validator_key.json` with production
  consensus params (15MB max block size)
- Write all entities as synthetic blocks to `<data-dir>/core/<chain-id>/`
- Write CometBFT `state.db` and `blockstore.db`
- Update `genesis.json` with the migration address and end height
- Print next-steps instructions for running a node

## Usage

```
genesis-writer write \
  --src-dsn          <audius_dp_clone_dsn>            \  # required
  --data-dir         <root_data_directory>             \  # required if --dst-dsn omitted
  [--dst-dsn         <core_chain_dsn>]                 \  # optional: use external postgres
  [--private-key     <hex_migration_key>]              \  # optional: auto-generated if omitted
  [--genesis-file    <path/to/genesis.json>]            \  # optional: auto-generated if omitted
  [--priv-validator-key-file <path/to/priv_validator_key.json>] \  # optional: auto-generated if omitted
  [--network         prod|stage|dev]                   \  # default: prod
  [--chain-id        audius-mainnet-beta]                \  # default: audius-mainnet-beta
  [--genesis-time    2025-01-01T00:00:00Z]             \  # default: now
  [--max-txs-per-block 10000]                          \  # default: 10000
  [--batch-size      1000]                             \  # default: 1000
  [--run-migrations]                                   \  # auto-enabled for managed postgres
  [--resume]                                           \  # resume interrupted run
  [--skip-users] [--skip-wallets] [--skip-tracks] [--skip-playlists] \
  [--skip-social] [--skip-plays] [--skip-apps] [--skip-comments] \
  [--skip-emails] [--skip-tip-reactions]
```

### Flags reference

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--src-dsn` | `GENESIS_SRC_DSN` | — | Source DP PostgreSQL DSN (**required**) |
| `--data-dir` | `GENESIS_DATA_DIR` | — | Root data directory. CometBFT state → `<data-dir>/core/<chain-id>/`, postgres → `<data-dir>/postgres/` |
| `--dst-dsn` | `GENESIS_DST_DSN` | — | Target Core chain PostgreSQL DSN. If omitted, a local postgres is started at `<data-dir>/postgres/` |
| `--private-key` | `GENESIS_MIGRATION_PRIVATE_KEY` | auto-generated | Genesis migration Ethereum key (hex, with or without `0x`). If omitted, a key is generated and saved to `<cmt-home>/genesis_migration_key.hex` for resume |
| `--network` | `NETWORK` | `prod` | EIP-712 signing domain (`prod`, `stage`, `dev`) |
| `--chain-id` | `CHAIN_ID` | `audius-mainnet-beta` | Core chain ID |
| `--genesis-time` | `GENESIS_TIME` | now | Chain genesis timestamp (RFC3339) |
| `--genesis-file` | `GENESIS_FILE` | `<data-dir>/core/<chain-id>/config/genesis.json` | Path to CometBFT `genesis.json`. Auto-generated with production consensus params if it doesn't exist |
| `--priv-validator-key-file` | `PRIV_VALIDATOR_KEY_FILE` | `<data-dir>/core/<chain-id>/config/priv_validator_key.json` | Path to `priv_validator_key.json`. Auto-generated if it doesn't exist |
| `--max-txs-per-block` | `GENESIS_MAX_TXS_PER_BLOCK` | `10000` | Transactions per synthetic block |
| `--batch-size` | `GENESIS_BATCH_SIZE` | `1000` | Rows fetched from source DB per query |
| `--run-migrations` | — | false | Apply the Core chain schema before writing (auto-enabled for managed postgres) |
| `--resume` | — | false | Resume from the last completed step of a previous run |
| `--skip-users` | `GENESIS_SKIP_USERS` | false | Skip user migration |
| `--skip-wallets` | `GENESIS_SKIP_WALLETS` | false | Skip associated wallets and dashboard wallet users |
| `--skip-tracks` | `GENESIS_SKIP_TRACKS` | false | Skip track migration |
| `--skip-playlists` | `GENESIS_SKIP_PLAYLISTS` | false | Skip playlist migration |
| `--skip-social` | `GENESIS_SKIP_SOCIAL` | false | Skip follows, saves, reposts, subscriptions, muted users |
| `--skip-plays` | `GENESIS_SKIP_PLAYS` | false | Skip play migration |
| `--skip-apps` | `GENESIS_SKIP_APPS` | false | Skip developer apps and grants |
| `--skip-comments` | `GENESIS_SKIP_COMMENTS` | false | Skip comments and comment reactions |
| `--skip-emails` | `GENESIS_SKIP_EMAILS` | false | Skip encrypted emails and email access grants |
| `--skip-tip-reactions` | `GENESIS_SKIP_TIP_REACTIONS` | false | Skip tip reactions |

### Data directory layout

When using `--data-dir`, the genesis-writer produces the same directory
layout as a production node:

```
<data-dir>/
├── core/
│   └── <chain-id>/
│       ├── config/
│       │   ├── genesis.json
│       │   └── priv_validator_key.json
│       └── data/
│           ├── blockstore.db/
│           ├── state.db/
│           └── priv_validator_state.json
└── postgres/
    └── <postgres data files>
```

### Managed postgres

When `--dst-dsn` is omitted, the genesis-writer starts its own PostgreSQL
instance at `<data-dir>/postgres/` using the system's `pg_ctl`. It:

- Initializes a new cluster if none exists (`initdb`)
- Starts an existing cluster if stopped
- Reuses an already-running cluster
- Configures for bulk-load performance (fsync off, large WAL, trust auth)
- Runs Core chain schema migrations automatically
- Stops the cluster on exit

The managed postgres runs on port 5440 to avoid conflicts with system postgres.

### Resume

Pass `--resume` to pick up from a previous interrupted run. The writer
tracks completed steps in a `genesis_writer_progress` table and recovers
chain state (height, app hash, block hash) from the database. The
auto-generated migration key is persisted to disk and reloaded on resume.

## Indexer integration

Indexers that consume the Core chain must handle `ManageEntityLegacyMigration`
transactions. When this transaction type is encountered, the indexer should:

1. Verify that `signer` matches the genesis migration authority configured in
   `genesis.json` (`genesis_migration_address`).
2. Apply entity data directly from `metadata` — the same JSON structure as
   `ManageEntityLegacy`, with `action` values `Create`, `Follow`, `Save`, `Repost`.
3. **Skip** all standard checks that do not apply to historical migration data:
   ownership validation, wallet uniqueness, handle filtering, character limits,
   entity ID offset checks, and social action signer checks.

For users, the `wallet` field in the metadata JSON is the user's real Ethereum
address and must be stored as-is (not derived from `signer`).

## Integration test

The integration test runs the full pipeline end-to-end:

1. Runs `genesis-writer` against a seeded source DB (in-process)
2. Reads all `ManageEntityLegacyMigration` and `TrackPlays` transactions from
   the core DB with a lightweight in-process indexer
3. Compares every entity (users, tracks, playlists, follows, saves, reposts, play count)
   against the original source data
4. Starts an `openaudio-1` node pointed at the pre-populated Core DB
5. Verifies the node advances the chain beyond the genesis height (consensus works)

No discovery-provider is required.

### Prerequisites

Build the core node image from the repo root:

```bash
make docker-dev
```

### Running the test

```bash
# 1. Create a directory for persistent test data
export EXT_DATA_DIR=$(mktemp -d)

# 2. Start the infrastructure services (postgres DBs, ganache, nginx)
docker compose -f cmd/genesis-writer/docker-compose.yml up -d \
  src-db core-db eth-ganache ingress

# 3. Wait for the DBs to be healthy, then run the test
go test -v -tags integration -run TestGenesisWriter -timeout 30m \
  ./cmd/genesis-writer/...

# 4. Tear down all services and volumes when done
docker compose -f cmd/genesis-writer/docker-compose.yml down -v
```

The test starts `openaudio-1` (via `docker compose up --profile chain`) after
`genesis-writer` has finished writing, so there is no race between them.

### Environment overrides

| Variable | Default | Description |
|----------|---------|-------------|
| `GENESIS_SRC_DSN` | `postgres://postgres:postgres@localhost:5435/genesis_writer_source?sslmode=disable` | Source DP DB |
| `GENESIS_DST_DSN` | `postgres://postgres:postgres@localhost:5436/openaudio?sslmode=disable` | Core chain DB |
| `GENESIS_CHAIN_URL` | `https://node1.oap.devnet` | Chain gRPC/HTTP URL |
| `GENESIS_WRITER_DATA_DIR` | auto (temp dir) | Where CometBFT state files are written and mounted into openaudio-1 |

### Ports

| Service | Host port |
|---------|-----------|
| Source DB (postgres) | `5435` |
| Core DB (postgres) | `5436` |
| Core node (gRPC) | `50052` |
| Ingress (HTTPS) | `443` |

### Test seed data

The seed database (`testdata/seed.sql`) exercises every entity type and
nullable column combination:

| Entity | Count | Variations |
|--------|-------|------------|
| Users | 6 | All fields filled; social handles only; all nullable fields NULL; fan accounts |
| Tracks | 9 | Public; unlisted; stream-gated; download-gated; remix; stem; with release date / preview / ISRC / BPM; NULL title+genre edge case |
| Playlists | 4 | Public album; private playlist; stream-gated; album with release date |
| Follows | 8 | Various follower/followee pairs including mutual follows and fan-to-fan |
| Saves | 5 | Track saves; album saves (`save_type='album'`, normalized to `'playlist'`); playlist saves |
| Reposts | 5 | Track and playlist reposts |
| Plays | 8 | With/without `user_id`; full geo (city/region/country); partial; none |

## Known limitations

### `is_verified` (artist badge)

`is_verified` is included in the user metadata JSON as `"is_verified": true`.
Indexers that process `ManageEntityLegacyMigration` must read and apply this
field directly — it cannot be set via a standard `ManageEntityLegacy` Create
action (which treats it as immutable), so the migration transaction type is the
correct place to carry it.

genesis-writer currently emits `is_verified` in the user Create metadata. An
indexer implementing `ManageEntityLegacyMigration` support must apply it during
the initial Create pass.

### Timestamps and `created_at`

Block timestamps are synthetic (sequential seconds from genesis time), not
the original Audius timestamps. To preserve real creation dates, the
genesis-writer includes a `"created_at"` field in each entity's metadata
JSON. Indexers should use this field — not the block or transaction
timestamp — when displaying entity creation dates.

### Current state only

Only the final current state of each entity is written; no intermediate update
history is preserved. The indexed state matches production final state.
