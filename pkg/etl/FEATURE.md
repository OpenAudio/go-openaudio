# ETL Package – Feature Context

## What This Is

Standalone Go package (`github.com/OpenAudio/go-openaudio/etl`) that indexes OpenAudio blockchain data into PostgreSQL. **Indexer-only**: no query API. Used by go-openaudio via a wrapper; also importable by external projects.

## Architecture

```
go-openaudio (main module)
├── cmd/openaudio/main.go      # Wires Indexer + Location + etlserver
├── pkg/etlserver/             # Wrapper: Indexer + LocationService + ETL ConnectRPC handlers
├── pkg/location/              # Geodb for city/country lat-long (explorer play maps)
├── pkg/console/              # Explorer UI; uses etlserver.GetDB(), GetLocationDB()
├── examples/etl-local/       # Standalone local runner for manual testing
└── pkg/etl/                  # THIS PACKAGE (submodule)
    ├── go.mod                 # Module github.com/OpenAudio/go-openaudio/etl
    ├── etl.go                 # Indexer struct, New(), setters
    ├── indexer.go             # Run(), indexBlocks() loop, tx type switch
    ├── schema.go              # Tx type constants (re-exports from processors)
    ├── config.go              # Enable/disable MV refresh, pg notify
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
├── handler.go           # Handler interface, Params, Dispatcher, constants
├── handler_test.go      # Dispatcher unit tests
├── validate.go          # Shared validators (ValidateSigner, ValidateHandle, etc.)
├── testutil_test.go     # Test helpers: setupTestDB, seeders, tx builders, assertions
├── user_create.go       # Example: User Create handler
├── user_create_test.go  # Tests for User Create
└── <entity>_<action>.go # One file per entity/action pair
```

### Types

- `Handler` interface: `EntityType() string`, `Action() string`, `Handle(ctx, params) error`
- `Params`: UserID, EntityID, EntityType, Action, Signer, Metadata, BlockNumber, BlockTime, TxHash, DBTX
- `Dispatcher`: Routes `(entity_type, action)` pairs to registered handlers
- `ValidationError`: Returned when validation fails (tx is skipped)

### Entity/Action Roadmap

**Done:** Foundation (handler framework, dispatcher, test infra, migrations, debug logging), User Create, User Update, User Verify

**Planned (one PR each):**
- Track Create, Track Update, Track Delete
- Playlist Create, Playlist Update, Playlist Delete
- Follow/Unfollow, Save/Unsave, Repost/Unrepost
- DeveloperApp (Create, Update, Delete)
- Grant (Create, Delete, Approve, Reject)
- Comments, Notifications, AssociatedWallet, DashboardWalletUser, etc.

## Key Integration Points

| Consumer | Uses |
|----------|-----|
| `cmd/openaudio/main.go` | `etl.New()`, `location.NewLocationService()`, `etlserver.NewETLService(indexer, locationDB, logger)` |
| `OPENAUDIO_ETL_ENABLED` | Enables ETL service + explorer (if `OPENAUDIO_EXPLORER_ENABLED`) |
| External projects | `etl.New(client, logger)`, `SetDBURL()`, `Run()` – connect to rpc.audius.co or grpc.audius.co |
| Local testing | `go run ./examples/etl-local --rpc <url> --db <postgres_url>` |

## Database

### ETL tables (migration 0001)
etl_blocks, etl_transactions, etl_plays, etl_manage_entities, etl_addresses, etl_validators, etl_sla_rollups, etl_sla_node_reports, etl_storage_proofs, etc.

### Entity manager domain tables (migration 0002)
users, tracks, playlists, follows, saves, reposts, track_routes, playlist_routes, developer_apps, grants, blocks.

Schema matches discovery-provider exactly (same table names, columns, composite PKs, enums). This enables the cutover strategy: stop celery indexer, start ETL indexer, same database.

Migrations are embedded and run automatically via `Indexer.Run()` → `db.RunMigrations()`.

## Testing

### Unit tests

```bash
cd pkg/etl && go test ./processors/entity_manager/... -v
```

Set `ETL_TEST_DB_URL` for database-backed tests:

```bash
ETL_TEST_DB_URL="postgres://localhost:5432/etl_test?sslmode=disable" \
  go test ./processors/entity_manager/... -v
```

Test helpers in `testutil_test.go`:
- `setupTestDB(t)` – runs migrations (down then up), returns pgxpool
- `seedUser(t, pool, userID, wallet, handle)` – insert prerequisite user
- `seedTrack(t, pool, trackID, ownerID)` – insert prerequisite track
- `buildParams(t, pool, ...)` – create Params ready for handler testing
- `mustHandle(t, handler, params)` / `mustReject(t, handler, params, wantSubstr)` – assertions

### Local runner

```bash
go run ./examples/etl-local \
  --rpc https://core.audius.co \
  --db "postgres://localhost:5432/etl_local?sslmode=disable"
```

Uses debug-level logging to show every transaction's payload and processing result.

## Module Notes

- **Submodule**: `pkg/etl/go.mod` with `replace github.com/OpenAudio/go-openaudio => ../..`
- **Proto dependency**: ETL imports `github.com/OpenAudio/go-openaudio/pkg/api/core/v1` (parent module)
- **Dockerfile**: Must `COPY pkg/etl/go.mod pkg/etl/go.sum pkg/etl/` before `go mod download`
