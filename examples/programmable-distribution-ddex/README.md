# Programmable Distribution Example (DDEX/ERN)

This example demonstrates programmable distribution using DDEX ERN. Same flow as the Entity Manager example, but uploads via DDEX instead of ManageEntity.

## How it Works

1. Uploads a demo track via DDEX ERN
2. Runs an HTTP server that signs stream URLs via GetStreamURLs
3. On `GET /stream`, signs and redirects to the content node's cidstream URL
4. On `GET /stream-no-signature`, redirects without signature (returns 401 from node)

## Setup

Start the local devnet:

```bash
make up
```

## Usage

```bash
make example/programmable-distribution-ddex
```

Or with flags:

```bash
cd examples/programmable-distribution-ddex && go run . -validator node1.oap.devnet -port 8800
```

## Testing

```bash
# Hit the worker, which signs and redirects to the stream
curl -L "http://localhost:8800/stream"

# No-signature route - redirects to node without signature (returns 401)
curl -L "http://localhost:8800/stream-no-signature"
```

## Requirements

- Running OpenAudio validator endpoint
- Demo audio file is read from `../../pkg/integration_tests/assets/anxiety-upgrade.mp3`
