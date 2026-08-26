# AGENTS.md — go-openaudio

Shared repository guidance for Claude, Codex, and human contributors.

## Terminology

The Open Audio Protocol uses a single canonical node type: the **Open Audio Validator Node**. The earlier *Discovery Node* / *Content Node* / *Creator Node* split is deprecated — both roles are now served by validator nodes. When writing new code, docs, comments, error messages, or commit messages, use "Open Audio Validator Node" (or "validator node"). Do not introduce new references to *Discovery Node*, *Content Node*, *Creator Node*, or *Discovery Provider*. Existing identifiers such as `creatorNodeEndpoint`, `audius_discprov_url`, and `ServiceProviderFactory` are retained for on-chain / env-var compatibility only and do not reflect the current architecture.

## Overview

OpenAudio Protocol (go-openaudio) is a Go implementation of the Audius decentralized music distribution protocol. The system is built around the **Open Audio Validator Node** — a single node type that runs both consensus (CometBFT-based blockchain) and storage services (Mediorum) for decentralized audio content.

## Build and Test Commands

### Build
```bash
# Build native binary
make bin/openaudio-native

# Build for Linux (x86_64)
make bin/openaudio-x86_64-linux

# Build for Linux (ARM64)
make bin/openaudio-arm64-linux

# Build Docker dev image
make docker-dev
```

### Run Local Development Environment
```bash
# Start 4-node local devnet
make up

# Stop and cleanup devnet
make down
```

The devnet creates 4 nodes accessible at:
- https://node1.oap.devnet
- https://node2.oap.devnet
- https://node3.oap.devnet
- https://node4.oap.devnet

Before running the devnet, add to `/etc/hosts`:
```bash
echo "127.0.0.1       node1.oap.devnet node2.oap.devnet node3.oap.devnet node4.oap.devnet" | sudo tee -a /etc/hosts
```

Then add the local dev x509 cert to your keychain so you will have green ssl in your browser.

```bash
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain dev/tls/cert.pem
```

### Testing
```bash
# Run all tests
make test

# Run unit tests only
make test-unit

# Run mediorum (storage) unit tests
make test-mediorum

# Run integration tests
make test-integration

# Cleanup after failed tests
make test-down
```

### Code Generation
Code generation is required when modifying SQL, Protocol Buffers, or Templ templates:

```bash
# Regenerate all code
make gen

# Regenerate SQL code from .sql files (using sqlc)
make regen-sql

# Regenerate protobuf code from .proto files
make regen-proto

# Regenerate templ HTML templates
make regen-templ

# Regenerate Ethereum contract bindings
make regen-contracts
```

**Important**: When editing `.sql` files in `pkg/core/db/sql/` or `pkg/eth/db/sql/`, run `make regen-sql` to regenerate Go code. When editing `.proto` files in `proto/`, run `make regen-proto`. When editing `.templ` files in `pkg/core/console/`, run `make regen-templ`.

### Linting
```bash
# Run linter
make lint

# Run linter with auto-fix
make lint-fix
```

### Pull Requests

PR titles must use Conventional Commits because this repo squash-merges PRs and lints the PR title. Use the form `<type>(optional-scope): <short description>`.

Allowed types are `feat`, `fix`, `perf`, `revert`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`, and `deps`. Good examples: `feat(console): show store-all node status`, `fix(storage): respect archive disk headroom`, `docs: clarify release workflow`. Do not use sentence-style PR titles like `Show store-all node status in console`.

## Architecture

### High-Level Components

1. **Core** (`pkg/core/`): CometBFT-based blockchain consensus layer
   - Manages validator consensus, block production, and transaction processing
   - Implements Proof of Useful Work (PoUW) for validators
   - Handles node registration and peer management
   - Runs on ports 26656 (P2P), 26657 (RPC), 26659 (custom API)

2. **Mediorum** (`pkg/mediorum/`): Optional media storage service
   - Decentralized blob storage for audio files and metadata
   - Implements replication across multiple nodes (default: 4x replication)
   - Supports multiple storage backends (local filesystem, S3, GCS)
   - Enabled on validator nodes that opt in to storage; legacy *discovery node* registrations run core only
   - Runs on port 1991

3. **ETH Bridge** (`pkg/eth/`): Ethereum integration layer
   - Syncs on-chain registry data from Ethereum L1
   - Manages validator registration and staking information
   - Tracks service provider (SP) information

4. **API Layer** (`pkg/api/`): gRPC/Connect services
   - Generated from Protocol Buffers definitions in `proto/`
   - Exposes both gRPC and REST endpoints via ConnectRPC
   - Main services: CoreService, StorageService, SystemService, EthService

5. **Console** (`pkg/core/console/`): Web UI dashboard
   - Built with Templ templates (Go-based HTML templating)
   - Accessible at `/console/` endpoints
   - Shows node status, blocks, transactions, peers, uptime

### Key Services

The main entry point (`cmd/openaudio/main.go`) starts multiple services:
- **audiusd-echo-server**: HTTP/HTTPS server with reverse proxies (ports 80/443)
- **core**: CometBFT blockchain node
- **mediorum**: Storage service (only if storage is enabled)
- **uptime**: Uptime tracking (disabled for localhost)
- **eth**: Ethereum bridge service

### Service Coordination

- Services communicate via gRPC/Connect on port 50051 (h2c - HTTP/2 without TLS)
- HTTP server (Echo framework) proxies requests to backend services
- `pos.PoSRequest` channel coordinates Proof of Stake operations between services
- The `CoreService` is shared across components and set via `SetCore()` after initialization

### Node Type

The Open Audio Protocol has a single canonical node type — the **Open Audio Validator Node** — that runs both core consensus and mediorum storage. The two registration modes below are legacy: they exist because some operators are still registered under the older Audius split-node model, and the binary continues to honor those env vars for compatibility.

**Open Audio Validator Node** (canonical)
- Identified by `OPENAUDIO_NODE_ENDPOINT` env var (legacy: `nodeEndpoint`)
- Runs both core consensus and mediorum storage
- Store and serve audio content
- Sole supported node type going forward

**Legacy *Content Node* registration** (deprecated — same software, different env vars)
- Identified by `creatorNodeEndpoint` env var
- Behaves as a validator node with storage enabled
- Retained for operators not yet re-registered

**Legacy *Discovery Node* registration** (deprecated — same software, storage disabled)
- Identified by `audius_discprov_url` env var
- Runs core only (consensus + indexing); no storage
- Retained for operators not yet re-registered

### Database

- PostgreSQL for persistent state (both core and mediorum)
- BoltDB for CometBFT state and blockstore (embedded key-value store)
- Pebble for additional key-value storage needs
- SQL migrations in `pkg/core/db/sql/migrations/` and `pkg/eth/db/sql/migrations/`
- SQLC generates type-safe Go code from SQL queries

### Configuration

All environment variables support an `OPENAUDIO_`-prefixed canonical name (preferred) with fallback to legacy names for backward compatibility. The `pkg/env` package provides helpers for this pattern (`Get`, `String`, `Bool`, `GetInt`, `GetDuration`, `Lookup`, `IsSet`).

- **Open Audio Validator Node** (canonical): `OPENAUDIO_NODE_ENDPOINT`, `OPENAUDIO_DELEGATE_PRIVATE_KEY`, `OPENAUDIO_DELEGATE_WALLET`, `OPENAUDIO_OWNER_WALLET`
- Legacy *content node* registration (deprecated): `creatorNodeEndpoint`, `delegatePrivateKey`, `delegateOwnerWallet`, `spOwnerWallet`
- Legacy *discovery node* registration (deprecated): `audius_discprov_url`, `audius_delegate_private_key`, `audius_delegate_owner_wallet`
- **Network**: `OPENAUDIO_ENV` (prod/stage/dev)
- **Database**: `OPENAUDIO_DB_URL`
- **Storage**: `OPENAUDIO_STORAGE_DRIVER_URL` (local/s3/gcs)
- **TLS**: `OPENAUDIO_TLS_DISABLED`, `OPENAUDIO_TLS_SELF_SIGNED`

Genesis configurations are in `pkg/core/config/genesis/` as JSON files.

## Development Patterns

### Protocol Buffers
- Definitions: `proto/` directory
- Generated code: `pkg/api/` directory
- Use `make regen-proto` after changes
- ConnectRPC provides both gRPC and REST endpoints from same definitions

### SQL Queries
- Write queries in `pkg/core/db/sql/*.sql` or `pkg/eth/db/sql/*.sql`
- SQLC config: `sqlc.yaml` files in each db directory
- Generated code appears in same directory as `.sql.go` files
- Use `make regen-sql` after changes

### Templ Templates
- HTML templates using Go: `pkg/core/console/*.templ`
- Generates `*_templ.go` files
- Use `make regen-templ` after changes

### Testing
- Unit tests: `*_test.go` files alongside source
- Integration tests: `pkg/integration_tests/`
- Tests run in Docker containers via docker-compose profiles

### Examples
Examples in `examples/` demonstrate SDK usage:
- `upload/`: Upload content to the network
- `indexer/`: Index blockchain data
- `programmable-distribution/`: Implement custom distribution logic

Run with: `go run ./examples/{example}/main.go` (requires devnet running)

### SDK
`pkg/sdk/` provides client libraries for interacting with nodes:
- `sdk.go`: Main SDK client
- `release.go`: Release management
- `rewards/`: Rewards queries
- `mediorum/`: Storage operations

### Hot Reloading
The dev Docker image supports hot reloading by mounting source directories:
```bash
docker run --rm -it \
  -p 80:80 -p 443:443 \
  -v $(pwd)/cmd:/app/cmd \
  -v $(pwd)/pkg:/app/pkg \
  audius/openaudio:dev
```

## Important Notes

- The codebase uses Go 1.25 (see `go.mod`)
- Main binary: `cmd/openaudio/main.go`
- Health check endpoint: `/health-check` (returns JSON with core, storage, git SHA, uptime)
- CometBFT version: 1.0.0
- Echo web framework for HTTP server
- Ethereum integration via go-ethereum (geth)
- Storage abstraction via gocloud.dev (supports S3, GCS, local filesystem)
