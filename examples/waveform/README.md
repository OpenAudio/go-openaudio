# Waveform peaks viewer

Renders a validator node's precomputed waveform peaks in
[wavesurfer.js](https://wavesurfer.xyz), beside wavesurfer decoding the same
audio itself. The point of the pair is to show that 750 bytes fetched from
`/waveform/:cid` describe the same track as several megabytes of mp3.

Requires a devnet node with `OPENAUDIO_WAVEFORM_ENABLED=true` — `dev/env/openaudio-1.env`
and `openaudio-2.env` set it.

```bash
make up
go run ./examples/waveform
```

Then open http://localhost:8777, pick an audio file, and press **Upload to node**.
The page uploads it, waits out the transcode and the analysis, and draws both panels
from that one file. To look at something already on the node, paste a 320 CID instead,
or pass one in the URL as `?cid=…`.

```bash
go run ./examples/waveform --node https://node2.oap.devnet --addr :9000
```

## What you should see

The two panels will not be identical, and should not be. The node stores per-bucket
**RMS** — average energy — while wavesurfer's default rendering draws per-bucket
**peak**, the loudest single sample in the bucket. Peak rendering is spikier and
reaches full height more often; RMS is flatter. Each is normalized to its own
maximum, so absolute heights are not comparable either. The overall envelope is
what should agree.

## Why the audio is not proxied

Streaming audio from a node requires a signed request; the waveform does not. That
asymmetry is the feature — a client can draw the waveform long before it is in a
position to stream anything — so the reference panel decodes the file from local disk
rather than pulling it back out of the node.
