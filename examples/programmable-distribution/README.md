# Programmable Distribution Example (Entity Manager)

Demonstrates a worker that signs stream URLs for entity manager tracks. The worker uploads a track with itself as the sole signer, then serves requests by signing and redirecting to the content node.

## How it Works

1. Uploads a demo track via ManageEntity with `access_authorities: [workerAddress]`
2. Runs an HTTP server that signs stream URLs
3. On `GET /stream`, the worker signs with its key and redirects to `/tracks/stream/:trackId?signature=...` on the node
4. The node validates the signature against `management_keys` and serves the audio

**Signature**: `/tracks/stream/:trackId` requires a valid signature from a `management_keys` signer. The worker provides that when you hit `/stream`. Without the worker's signature, the stream returns 401. (Note: `/content/:cid` is open and does not require a signature.)

## Setup

Start the local devnet:

```bash
make up
```

## Usage

```bash
make example/programmable-distribution
```

Or with flags:

```bash
cd examples/programmable-distribution && go run . -validator node1.oap.devnet -port 8800
```

## Testing

```bash
# Hit the worker, which signs and redirects to the track stream
curl -L "http://localhost:8800/stream"

# No-signature route - redirects to node without signature (returns 401)
curl -L "http://localhost:8800/stream-no-signature"
```

## Follow-Gated (Future)

For follow-gated access, the worker would call the Audius API to verify the user follows the artist before signing. The data flow is the same; only the condition check is added.
