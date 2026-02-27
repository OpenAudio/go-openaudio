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

```
uploaded cid: <transcoded_cid>
tx receipt: <tx_hash>
```
