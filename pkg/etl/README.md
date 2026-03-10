# ETL Package

Standalone, importable Go package for indexing OpenAudio blockchain data into PostgreSQL.

## Usage

```go
import (
    "net/http"
    "github.com/OpenAudio/go-openaudio/etl"
    corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
)

// Connect to rpc.audius.co (HTTP/Connect) or grpc.audius.co
client := corev1connect.NewCoreServiceClient(
    http.DefaultClient,
    "https://rpc.audius.co",
)

indexer := etl.New(client, logger)
indexer.SetDBURL("postgres://user:pass@localhost:5432/etl?sslmode=disable")
indexer.Run() // blocks until done or error
```

## Configuration

- `SetDBURL(url)` - PostgreSQL connection string
- `SetStartingBlockHeight(n)` - Start from block n (default: 1)
- `SetEndingBlockHeight(n)` - Stop after block n (0 = run forever)
- `SetRunDownMigrations(true)` - Run down migrations before up
- `SetCheckReadiness(true)` - Wait for core service ready before starting
- `SetConfig(c)` - Enable/disable optional components (MV refresh, pg notify)

## Tests

```bash
cd pkg/etl && go test ./...
cd pkg/etl && go test ./processors/... -v
```
