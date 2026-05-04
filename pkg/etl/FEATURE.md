# ETL Package – Feature Context

## What This Is

Standalone Go package (`github.com/OpenAudio/go-openaudio/etl`) that indexes OpenAudio blockchain data into PostgreSQL. **Indexer-only**: no query API. Used by go-openaudio via a wrapper; also importable by external projects.

## Architecture

```
go-openaudio (main module)
├── cmd/openaudio/main.go      # Wires Indexer + Location + etlserver
├── pkg/etlserver/             # Wrapper: Indexer + LocationService + ETL ConnectRPC handlers
├── pkg/location/              # Geodb for city/country lat-long (explorer play maps)
├── pkg/explorer/              # Explorer UI (templ); uses etlserver.GetDB(), GetLocationDB()
├── examples/etl/              # Standalone local runner for manual testing
└── pkg/etl/                   # THIS PACKAGE (submodule)
    ├── go.mod                 # Module github.com/OpenAudio/go-openaudio/etl
    ├── etl.go                 # Indexer struct, New(), setters
    ├── indexer.go             # Run(), indexBlocks() loop, tx type switch
    ├── schema.go              # Tx type constants (re-exports from processors)
    ├── config.go              # Enable/disable MV refresh, pg notify, data type gating
    ├── db/                    # sqlc, migrations
    └── processors/
        ├── processor.go       # Processor interface; play.go, manage_entity.go
        └── entity_manager/    # Entity manager handlers (see below)
```

**Data flow**: Core RPC (GetBlock) → Indexer → processors → PostgreSQL. Console queries DB via `etlserver.GetDB()`.

## Entity Manager

The entity manager subsystem processes `ManageEntity` transactions into domain tables (users, tracks, playlists, etc.), implementing full validation parity with the discovery-provider celery indexer.

### Processing Flow

```
ManageEntity tx
  ├── 1. Raw append to etl_manage_entities (existing, always runs)
  └── 2. Dispatcher → entity_manager/<entity>_<action>.go
       ├── Stateless validation (format, limits, offsets)
       ├── Stateful validation (existence, ownership, uniqueness)
       └── Domain table write (users, tracks, playlists, etc.)
```

ValidationError = skip this tx (logged at debug level, non-fatal).

### File Layout

```
processors/entity_manager/
├── handler.go               # Handler interface, Params, Dispatcher, constants
├── handler_test.go          # Dispatcher unit tests
├── validate.go              # Shared validators (ValidateSigner, ValidateHandle, etc.)
├── testutil_test.go         # Test helpers: setupTestDB, seeders, tx builders, assertions
├── genre_allowlist.go       # Genre validation allowlist
├── slug.go                  # Slug sanitization + collision resolution (tracks & playlists)
│
├── user_create.go           # User Create
├── user_update.go           # User Update (markNotCurrent, metadata merge)
├── user_verify.go           # User Verify
│
├── track_create.go          # Track Create
├── track_update.go          # Track Update
├── track_delete.go          # Track Delete
├── track_row.go             # Track row struct, load/merge/insert helpers
├── track_queries.go         # Track existence queries
│
├── playlist_create.go       # Playlist Create
├── playlist_update.go       # Playlist Update
├── playlist_delete.go       # Playlist Delete
├── playlist_row.go          # Playlist row struct, load/merge/insert helpers
├── playlist_queries.go      # Playlist existence + slug queries
│
├── social_follow.go         # Follow / Unfollow
├── social_save.go           # Save / Unsave
├── social_repost.go         # Repost / Unrepost
│
├── developer_app_create.go  # DeveloperApp Create
├── developer_app_update.go  # DeveloperApp Update
├── developer_app_delete.go  # DeveloperApp Delete
│
├── grant_create.go          # Grant Create
├── grant_revoke.go          # Grant Delete / Approve / Reject
│
├── muted_user.go            # MutedUser Mute / Unmute
│
├── notification.go          # Notification Create / View, PlaylistSeen View
│
├── comment_create.go        # Comment Create
├── comment_update.go        # Comment Update
├── comment_delete.go        # Comment Delete
├── comment_react.go         # Comment React / Unreact
├── comment_pin.go           # Comment Pin / Unpin
├── comment_report.go        # Comment Report
├── comment_mute.go          # Comment Mute / Unmute (notification settings)
└── comment_queries.go       # Comment existence + reaction queries
```

### Types

- `Handler` interface: `EntityType() string`, `Action() string`, `Handle(ctx, params) error`
- `Params`: UserID, EntityID, EntityType, Action, Signer, Metadata, BlockNumber, BlockTime, TxHash, DBTX, Logger
- `Dispatcher`: Routes `(entity_type, action)` pairs to registered handlers
- `ValidationError`: Returned when validation fails (tx is skipped)

### Entity/Action Status

**All entity types implemented (53 handlers):**

**Implemented (53 handlers):**

| Entity | Actions |
|--------|---------|
| User | Create, Update, Verify |
| Track | Create, Update, Delete, Download, Mute, Unmute |
| Playlist | Create, Update, Delete |
| Follow | Follow, Unfollow |
| Save | Save, Unsave |
| Repost | Repost, Unrepost |
| Subscribe | Subscribe, Unsubscribe |
| Share | Share |
| DeveloperApp | Create, Update, Delete |
| Grant | Create, Delete, Approve, Reject |
| MutedUser | Mute, Unmute |
| Notification | Create, View |
| PlaylistSeen | View |
| Comment | Create, Update, Delete, React, Unreact, Pin, Unpin, Report, Mute, Unmute |
| AssociatedWallet | Create, Delete (ETH ecrecover + SOL ed25519 verification) |
| DashboardWalletUser | Create, Delete (ETH ecrecover verification) |
| Tip | Reaction |
| EncryptedEmail | Create |
| EmailAccess | Update |
| Event | Create, Update, Delete |

## Key Integration Points

| Consumer | Uses |
|----------|-----|
| `cmd/openaudio/main.go` | `etl.New()`, `location.NewLocationService()`, `etlserver.NewETLService(indexer, locationDB, logger)` |
| `OPENAUDIO_ETL_ENABLED` | Enables ETL service + explorer (if `OPENAUDIO_EXPLORER_ENABLED`) |
| `OPENAUDIO_ETL_ENTITY_MANAGER_DATA_TYPES` | Comma-separated list of entity types to index (nil = all) |
| External projects | `etl.New(client, logger)`, `SetDBURL()`, `Run()` – connect to rpc.audius.co or grpc.audius.co |
| Local testing | `go run ./examples/etl --rpc <url> --db <postgres_url> --start <block>` |

## Database

### ETL tables (migration 0001)
etl_blocks, etl_transactions, etl_plays, etl_manage_entities, etl_addresses, etl_validators, etl_sla_rollups, etl_sla_node_reports, etl_storage_proofs, etc.

### Entity manager domain tables (migration 0002)
users, tracks, playlists, follows, saves, reposts, track_routes, playlist_routes, developer_apps, grants, blocks.

### Additional migrations (0003–0015)
0003: User verify columns (twitter, instagram, tiktok handles).
0004: muted_users. 0005: notification. 0006: comment tables.
0007: subscriptions. 0008: shares. 0009: track_downloads.
0010: events. 0011: encrypted_emails, email_access.
0012: dashboard_wallet_users. 0013: user_tips, reactions.
0014: associated_wallets. 0015: core_indexed_blocks.

All migrations are idempotent (`IF NOT EXISTS`, `DO $$ EXCEPTION`, `DROP TRIGGER IF EXISTS`).

Schema matches discovery-provider exactly (same table names, columns, composite PKs, enums). This enables the cutover strategy: stop celery indexer, start ETL indexer, same database.

Migrations are embedded and run automatically via `Indexer.Run()` → `db.RunMigrations()`.

### Block Numbering and em_block Offset

Domain tables (users, tracks, playlists, follows, saves, reposts, etc.) have `blocknumber REFERENCES blocks(number)`. The `blocks` table uses a sequential numbering scheme (`blocks.number`) that differs from the CometBFT chain height. The mapping is tracked in `core_indexed_blocks`:

```
core_indexed_blocks(chain_id, height, em_block)
  - height:   CometBFT chain block height
  - em_block: blocks.number (the value domain tables FK to)
  - offset:   em_block - height (constant per chain_id)
```

At startup, the indexer queries `core_indexed_blocks` for the current chain ID to determine the offset. For existing databases with prior Python-indexed data, this continues the `blocks.number` sequence seamlessly. For clean databases, the offset is 0 (em_block = chain height).

**Known limitation:** Chain ID rollovers (e.g., `audius-mainnet-alpha` → `audius-mainnet-alpha-beta`) are not handled. The offset is looked up once at startup for the current chain ID. If the chain rolls over, the ETL must be restarted (and may need manual intervention to set the correct offset for the new chain). The Python discovery indexer has explicit chain rollover detection logic that the Go ETL does not yet replicate.

## Testing

### Unit tests

```bash
cd pkg/etl && go test ./processors/entity_manager/... -v
```

Set `ETL_TEST_DB_URL` for database-backed tests:

```bash
ETL_TEST_DB_URL="postgres://postgres:postgres@localhost:5432/etl_test?sslmode=disable" \
  go test ./processors/entity_manager/... -v
```

Test helpers in `testutil_test.go`:
- `setupTestDB(t)` – runs migrations (down then up), returns pgxpool
- `seedUser(t, pool, userID, wallet, handle)` – insert prerequisite user
- `seedTrack(t, pool, trackID, ownerID)` – insert prerequisite track
- `seedPlaylist(t, pool, playlistID, ownerID)` – insert prerequisite playlist
- `buildParams(t, pool, ...)` – create Params ready for handler testing
- `mustHandle(t, handler, params)` / `mustReject(t, handler, params, wantSubstr)` – assertions

### Local runner

```bash
go run ./examples/etl \
  --rpc https://rpc.audius.co \
  --db "postgres://postgres:postgres@localhost:5432/etl_local?sslmode=disable" \
  --start 22275839
```

Uses debug-level logging to show every transaction's payload and processing result.
Progress is logged at INFO level every 10 seconds (block height, tx counts).

### Parity testing (against production clone)

Standalone CLI tool in `pkg/etl/parity/` for comparing Go ETL output against existing Python-indexed data.

```bash
cd pkg/etl

# 1. Snapshot baseline (records max chain height + row counts)
go run ./parity snapshot --db "$ETL_DB_URL"

# 2. Run the ETL for some duration
go run ../../examples/etl --rpc https://rpc.audius.co --db "$ETL_DB_URL" --start <snapshot_max_block + 1>

# 3. Diff results
go run ./parity diff --db "$ETL_DB_URL"

# 4. Cleanup
go run ./parity cleanup --db "$ETL_DB_URL"
```

The diff cross-references `etl_manage_entities` (which logs every ManageEntity tx) with domain table growth to find match rates, and runs structural integrity checks (non-null required fields, FK targets exist, is_current consistency).

## Module Notes

- **Submodule**: `pkg/etl/go.mod` with `replace github.com/OpenAudio/go-openaudio => ../..`
- **Proto dependency**: ETL imports `github.com/OpenAudio/go-openaudio/pkg/api/core/v1` (parent module)
- **Dockerfile**: Must `COPY pkg/etl/go.mod pkg/etl/go.sum pkg/etl/` before `go mod download`
- **Binary caveat**: `go build ./examples/etl/` outputs an `etl` binary at repo root that conflicts with the `replace` directive. Use `go run` instead.
