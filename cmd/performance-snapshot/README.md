# Performance snapshot command

`performance-snapshot` is Core's deterministic batch path for producing and
signing weekly Solana node-performance reward snapshots. It is intentionally an
operator-invoked batch command, not a wall-clock scheduler: an epoch must not be
published until its Core block range and every consensus input are final.

Build it with:

```sh
make bin/performance-snapshot-native
```

## Finalized input source

The initial production adapter is a bounded, strict JSON file source. Operators
register the expected producer ID through `--source-id` or
`OPENAUDIO_PERFORMANCE_SOURCE_ID`; a manifest carrying any other ID is rejected.
The source producer must export the Ethereum-derived frozen operator, signer,
and weight set for the epoch plus these three per-operator ratios:

- proof-of-storage work;
- useful work;
- block-production work.

Every ratio requires `completed`, a nonzero `total`, and a nonzero evidence
hash. Missing records and zero totals fail generation; they are never treated
as successful work. `completed: 0` is valid only when `total` and the evidence
hash are present, and scores zero for that component.

Core does not currently contain an authoritative per-operator useful-work
counter. The command therefore requires the useful-work records to be committed
as a sorted-pair Keccak Merkle root and signed, using raw Keccak/secp256k1, by
strictly more than two thirds of the frozen eligible Ethereum signing weight.
The source ID, Core chain ID, finalized block hash, epoch boundaries, scoring
version, eligible root/weight, and useful-work root are all included in the
signed message. Duplicate, non-eligible, malformed, competing-root, or
insufficient-weight attestations fail closed.

The manifest is versioned as `schema_version: 1`:

```json
{
  "schema_version": 1,
  "source_id": "core-useful-work-v1",
  "chain_id": "audius-mainnet-beta",
  "epoch": {
    "id": 7,
    "start_unix": 1700000000,
    "end_unix": 1700604800,
    "start_block": 100,
    "end_block": 200
  },
  "scoring_version": "0x28823611c1c6d274a4d71ab65ade7629644dfc5be8459c8edceda54ae7d01d2b",
  "finalized_block_hash": "0x...32 bytes...",
  "finalized_block_height": 250,
  "operators": [
    {
      "operator": "0x...20 bytes...",
      "signer": "0x...20 bytes...",
      "weight": 100,
      "storage": {"completed": 1, "total": 1, "evidence_hash": "0x..."},
      "useful_work": {"completed": 4, "total": 5, "evidence_hash": "0x..."},
      "block_production": {"completed": 9, "total": 10, "evidence_hash": "0x..."}
    }
  ],
  "useful_work_consensus": {
    "root": "0x...32 bytes...",
    "attestations": [
      {"signer": "0x...20 bytes...", "signature": "...65-byte hex..."}
    ]
  }
}
```

The consensus producer can prepare and collect the input quorum with:

```sh
bin/performance-snapshot-native prepare-input \
  --input epoch-7.json \
  --source-id core-useful-work-v1

OPENAUDIO_DELEGATE_PRIVATE_KEY=... \
bin/performance-snapshot-native sign-input \
  --input epoch-7.json \
  --source-id core-useful-work-v1
```

`prepare-input` prints the exact useful-work root, eligible root/weight, message
bytes, and message hash. `sign-input` prints one eligible signer's record. The
collector stores a quorum of those records in `useful_work_consensus` without
changing the prepared root.

## Generate and publish

After the manifest reaches quorum, generate the immutable relayer artifact:

```sh
bin/performance-snapshot-native generate \
  --input epoch-7.json \
  --source-id core-useful-work-v1 \
  --program-id "$SOLANA_PERFORMANCE_REWARDS_PROGRAM_ID" \
  --config-account "$SOLANA_PERFORMANCE_REWARDS_CONFIG_ACCOUNT" \
  --output /data/performance-rewards/epoch-7.json \
  --print
```

The output is canonical indented JSON with no generation timestamp. Operator and
attestation ordering cannot change its bytes. Publication uses an atomic,
create-only path: repeating identical output is idempotent, while different
bytes at the same epoch path return an error and never overwrite the published
artifact. `--print` also writes those exact bytes to stdout for an artifact
publisher or dashboard ingestion job.

The artifact contains:

- the finalized Core input commitment and useful-work quorum weight;
- the complete scored snapshot, evidence hashes, leaves, and proofs;
- `open_first_epoch` / `open_epoch` arguments;
- the exact 251-byte raw-Keccak signer message and snapshot commitment for
  `attest_snapshot`;
- `finalize_first_snapshot` / `finalize_snapshot` arguments;
- every `claim` argument and Merkle proof.

A dashboard may serve the artifact unchanged. A relayer uses the named
instruction and `args` objects with the Anchor IDL, derives the existing
claimable-account recipient for each Ethereum operator, and supplies the normal
Solana accounts. The command does not hold a Solana payer or submit
transactions; that separation keeps snapshot consensus independent from a
particular relayer.

## Sign the Solana snapshot

Each eligible validator node validates the published artifact locally and emits
its Solana attestation payload:

```sh
OPENAUDIO_DELEGATE_PRIVATE_KEY=... \
bin/performance-snapshot-native sign-snapshot \
  --artifact /data/performance-rewards/epoch-7.json \
  --output /data/performance-rewards/epoch-7-attestation.json
```

The private key is read only from the named environment variable (default
`OPENAUDIO_DELEGATE_PRIVATE_KEY`), never from argv. Before signing, the command
recomputes all eligibility/reward leaves, roots, proofs, totals, instruction
arguments, input commitment, and exact Solana commitment bytes. It refuses keys
outside the frozen eligible set. The output includes the recovered Ethereum
signer, 65-byte signature, commitment message/hash, snapshot commitment, and
eligibility proof needed to construct the preceding Solana secp256k1 instruction
and `attest_snapshot` instruction.

## Required deployment configuration

- a consensus source producer ID shared by operators;
- a finalized Core block hash and frozen Ethereum identity/weight export;
- a quorum-signed useful-work record set with authoritative evidence hashes;
- the deployed Performance Rewards program ID and initialized config account;
- each validator's existing Ethereum delegate key for the two signing steps;
- an artifact publication location and a Solana relayer/dashboard that consumes
  the generated JSON.
