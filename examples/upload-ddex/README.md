# Upload Example (DDEX/ERN)

Uploads a track via DDEX ERN (Electronic Release Notification). Uses `Storage.UploadFiles` and creates an ERN release with the transcoded CID.

## Setup

Start the local devnet:

```bash
make up
```

## Usage

```bash
make example/upload-ddex
```

Or:

```bash
cd examples/upload-ddex && go run .
```

## Output

Prints the uploaded CID and ERN receipt. The track is available via ERN addresses. Use the programmable-distribution-ddex example for geo-fenced streaming with ERN-based tracks.
