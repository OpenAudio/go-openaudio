# Upload Example (Entity Manager)

Uploads a track via Entity Manager. Uploads audio to Mediorum, then creates a Track with ManageEntity.

## Setup

Start the local devnet:

```bash
make up
```

## Usage

```bash
make example/upload
```

Or from repo root:

```bash
cd examples/upload && go run .
```

## Output

Prints the track ID, CID, and signers. The track is streamable at:

```
https://node1.oap.devnet/tracks/stream/{track_id}?signature=<signed_by_signer>
```

Use the programmable-distribution example to get a signed stream URL.
