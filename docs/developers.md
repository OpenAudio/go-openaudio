# Development

```bash
# build a local node
make build-audiusd-local

# test
make build-audiusd-local
make build-audiusd-test
make mediorum-test
make core-test

# sync a locally built node to stage
docker run --rm -it -p 80:80 -p 443:443 -e NETWORK=stage audius/audiusd:local

# sync a locally built node to prod
docker run --rm -it -p 80:80 -p 443:443 audius/audiusd:local

# health check
curl http://localhost/health-check
curl -k https://localhost/health-check

# view in browser
open http://localhost/console/overview
open https://localhost/console/overview
```

## Native Development (macOS)

> WORK IN PROGRESS

To build and run audiusd natively on macOS without Docker:

### Prerequisites

1. Install system dependencies:
```bash
# Install PostgreSQL
brew install postgresql@15

# Install audio processing dependencies
brew install ffmpeg fftw libsndfile aubio opus libvorbis flac

# Install build tools
brew install go make
```

2. Start PostgreSQL service:
```bash
brew services start postgresql@15
```

### Building

1. Build the binary:
```bash
make bin/audiusd-native
```

2. Create a data directory:
```bash
mkdir -p ~/audiusd/data/postgres
```

3. Initialize the database:
```bash
initdb -D ~/audiusd/data/postgres
createdb audiusd
```

### Running

1. Start audiusd:
```bash
./bin/audiusd-native
```

2. Access the web interface:
```bash
open http://localhost/console/overview
```

### Troubleshooting

- If you get PostgreSQL connection errors, make sure the service is running:
```bash
brew services restart postgresql@15
```

- For audio processing errors, verify all libraries are installed:
```bash
brew list | grep -E 'ffmpeg|fftw|sndfile|aubio|opus|vorbis|flac'
```

- Check logs for detailed error messages:
```bash
tail -f ~/audiusd/logs/audiusd.log
```
