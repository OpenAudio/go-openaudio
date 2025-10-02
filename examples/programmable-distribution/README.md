# Programmable Distribution Example

This example demonstrates geolocation-based content distribution using the OpenAudio protocol. The service uploads a track and provides streaming access only to requests from a specific city (Bozeman, by default).

## How it Works

1. Uploads a demo track to the OpenAudio network
2. Runs an HTTP server that filters stream access by geolocation
3. Returns stream URLs only to requests from the allowed city

## Setup

Start the local devnet:

```bash
make up
```

## Usage

Run the example:

```bash
make example/programmable-distribution
```

Or run directly with custom flags:

```bash
cd examples/programmable-distribution
go run . -validator node3.openaudio.devnet -port 8800
```

### Flags

- `-validator` - Validator endpoint URL (default: `node3.openaudio.devnet`)
- `-port` - Server port (default: `8800`)

## Testing

Access the streaming endpoint with a city parameter:

```bash
# Allowed city (Bozeman)
curl "http://localhost:8800/stream-access?city=Bozeman"

# Blocked city
curl "http://localhost:8800/stream-access?city=Seattle"
```

## Requirements

- Running Audius validator endpoint
- Demo audio file is read from `../../pkg/integration_tests/assets/anxiety-upgrade.mp3`
