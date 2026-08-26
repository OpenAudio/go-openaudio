# Genesis migration rollout

Draft. The runbook is the plan; everything under Reference is the why, cited to
code. Open questions are at the end and are genuinely open.

---

# Runbook

Ordered. Steps 1–8 are reversible at low cost, 9–11 are reversible at the cost
of a re-sync, and step 12 is not reversible.

## Phase A — build the shippable chain

**1. Derive the bootstrap validator key from rickyrombo's delegate key.**

Not an arbitrary key from a vault. `ensurePrivValidator` (`config/setup.go:231`)
derives the CometBFT key from `OPENAUDIO_DELEGATE_PRIVATE_KEY`, so genesis must
list *that* key or rickyrombo comes up as a non-validator and the chain never
produces block 5,819. Take the existing
`<root>/audius-mainnet-alpha-beta/config/priv_validator_key.json`, or re-derive
it from the delegate key.

Signing on both chains with this key is safe — the chain ID is inside the vote
sign bytes, so old- and new-chain votes are not interchangeable, and
`priv_validator_state.json` is per-chain-directory.

**2. Re-run genesis-writer against prod** with
`--priv-validator-key-file` pointing at that key. ~10h. The current T7 artifact
is a verification artifact only: the validator set is hashed into every block
header, so swapping it means rebuilding the chain (Reference §3).

**3. Verify the new artifact** — replay + parity. The writer and ETL logic are
unchanged by the key swap, so this is confirming the fresh prod read, not
re-litigating the pipeline.

> **Seal the artifact before pointing anything at it.** The verification node,
> the ETL, and every other service take the chain DB as `OPENAUDIO_DB_URL`, and
> any of them can destroy it. A node whose binary does not embed *this*
> artifact's genesis does not fail — it takes its "generate a new genesis" path
> and runs the core migrations **down**, dropping every table the writer just
> filled. `OPENAUDIO_ENV=dev` loads the embedded `dev.json`, so a binary built
> before the genesis was copied in looks completely normal until it wipes a
> ten-hour write. This has happened once already, on 2026-08-25.
>
> So do not let a connection string reach the pristine artifact at all:
>
> ```sql
> ALTER DATABASE <artifact> WITH ALLOW_CONNECTIONS false;
> CREATE DATABASE <artifact>_serve TEMPLATE <artifact>;
> ```
>
> A sealed database still works as a `TEMPLATE` — that is how `template0`
> works — so sealing first leaves no window where it is reachable, and the copy
> is file-level (minutes, not a re-run). Point the node, the replay, and parity
> at `<artifact>_serve`. If that copy is damaged, make another from the
> template; the writer never runs twice.
>
> Verify the artifact **after** the services are up, not before. A count taken
> before you start a node certifies a state that the node may then change.

**4. Build the image, pinned — do not promote to `latest stable`.** Publishing
*is* the mass migration, since genesis is compiled in (`//go:embed prod.json`).
The image needs:

- `pkg/core/config/genesis/prod.json` ← new genesis
- `ProdPersistentPeers` (`config.go:55`) → new bootstrap node(s)
- `ProdStateSyncRpcs` (`config.go:85`) → the two new-chain snapshot servers.
  rickyrombo is **not** in this list today and must be added.

## Phase B — stand up the new network

**5. Seed rickyrombo.**

- **Back up its Postgres first.** Postgres is shared between chains and this
  step overwrites it; the backup is the rollback.
- Restore the writer's Postgres output over rickyrombo's database. The
  blockstore alone is not enough — the DB must hold new-chain state.
- Copy the writer's `data/` (blockstore, state, evidence).
- Env: `OPENAUDIO_ARCHIVE=true`,
  `OPENAUDIO_STATE_SYNC_SERVE_SNAPSHOTS=true`,
  `OPENAUDIO_STATE_SYNC_ENABLE=false` (it is the source, not a consumer),
  `OPENAUDIO_PERSISTENT_PEERS` overridden to the new set so it stops dialling
  old-network hosts. Leave `BlockInterval` at its default.
- Check free disk: snapshots silently skip below `SnapshotMinFreeBytes`
  (default **80 GiB**).

**6. Start it and confirm it produces block 5,819.** If it does not, the
validator key does not match genesis — that is the dead-chain failure mode, and
it looks like "waiting for comet to catch up" with height stuck at 0.

**7. Seed a second node the same way**, from the same artifact.

Ideally not one of `creatornode` / `rpc.audius.co` / `v.monophonic`, so all
three stay as old-chain rollback anchors — but rickyrombo is the only node
outside that list, so absent a fifth node one of the three has to move. That
still leaves two anchors, which is the minimum and has no slack.

Exclude from the copy: `node_key.json` (P2P identity must be unique — it is what
goes in `ProdPersistentPeers`), `priv_validator_key.json` and
`priv_validator_state.json` (it derives its own from its own delegate key, which
is not in genesis, so it runs as a full node). Copying the bootstrap's would
mean two nodes signing as the same 100%-power validator.

Seeding by copy is what avoids block-syncing 58M transactions, and it is the
only way to reach two nodes: state sync needs ≥2 RPC servers *and* a snapshot,
neither of which exists yet.

**8. Wait for height 100,000 (~26–37h) and verify a snapshot exists.**

Check `ListSnapshots` or the `Snapshot created` log line. Do not assume —
`snapshotHeight = latest - (latest % 100000)` silently yields 0 below 100,000,
so producing nothing is the default outcome. This wait doubles as the
dual-write soak.

## Phase C — cut over

**9. Turn on flushing.** `newChainFlushEnabled=true`, `NewChainURL` → bootstrap,
`NewChainFlushFromBlock` → the migration end height so backfill-covered rows are
skipped rather than re-sent. Queueing is already on. Reversible: the old chain
still has every write.

**10. Point the API indexer at the new chain.** Two hard requirements
(Reference §11):

- **Plumb a start height into the API indexer first — it cannot express this
  today.** `indexer/indexer.go` never calls `SetStartingBlockHeight`, so it
  resumes from `etl_blocks`, and that query has no `chain_id`. Pointed at the
  new chain as-is it would resume at the old chain's ~24M height against a chain
  at ~100,000 and wait for a block that will not exist for years — a silent
  stall, not an error.
- **Drain the flush queue before switching.** Writes still in flight land on the
  new chain *above* the switch point and get indexed a second time, having
  already been indexed from the old chain. `InsertPlay` is a bare insert with no
  `ON CONFLICT`, so that duplicates plays rather than upserting them.

Keep the old chain indexed continuously right up to the switch. Everything below
the new chain's tip is migration blocks or already-flushed writes, all of it
already in the API's database, so skipping it is correct.

**11. Promote the image to stable.** The fleet pulls it, creates a fresh
`audius-mainnet-beta` directory, and state-syncs from the two servers. Back up
each node's Postgres before it switches. Roll out in waves if the release
channel allows — every switched node is one whose rollback now costs a re-sync.

**12. Stop writing to the old chain. Point of no return.** Until this moment
the old chain has every write and rollback is real. After it, rollback loses
data. Announce it; do not let it happen as a side effect.

**13. Migrate the held-back anchors** once step 12 is settled and confidence is
high.

---

# Reference

## 1. Facts that shape the plan

| Fact | Where | Consequence |
|---|---|---|
| Genesis is compiled into the binary | `config/genesis/genesis.go` (`//go:embed prod.json`) | Switching chains **requires a release**. Binary ships before anything cuts over. |
| Chain ID changes: `audius-mainnet-alpha-beta` → `audius-mainnet-beta` | `genesis/prod.json:3` | Hard network split. CometBFT compares `NodeInfo.Network` at handshake, so old and new nodes **cannot** peer even when they dial each other. |
| Data dir is namespaced by chain ID | `config/setup.go:67` — `cometRootDir = RootDir/chainID` | New chain gets a **fresh directory**; old chain data is untouched. Rollback is "ship the old image". Cost: both chains on disk. |
| Prod genesis has 9 validators @ power 10; new genesis has 1 @ power 100 | `prod.json`, new `genesis.json` | One validator is 100% of voting power, so the bootstrap node commits blocks alone. See §3. |
| Two hardcoded peer lists | `config.go:55` (`ProdPersistentPeers`), `config.go:85` (`ProdStateSyncRpcs`) | Both point at old-network hosts. Both must change in the shipped image. |
| `ServeSnapshots` defaults **false** | `config.go:240` | Nobody serves snapshots unless explicitly enabled. |
| Snapshot interval defaults to 100,000 blocks | `config.go:243` | First snapshot at height 100,000. See §2. |
| Archive mode disables pruning | `data_companion.go` — `if s.config.Archive { return 0 }` | `OPENAUDIO_ARCHIVE=true` keeps full history. Only affects *future* pruning; it does not restore already-pruned blocks. |
| Mediorum presence is judged from the **bucket**, not the DB | `mediorum/server/repair.go:553-595` | A fresh mediorum DB does **not** trigger re-download. No blob egress from wiping it. |
| Empty blocks every 1s in prod | `setup.go:153` (`CreateEmptyBlocksInterval`; 200ms only on stage/dev) | Sets the clock for §2. |

## 2. State sync: what it carries, and when the first one appears

A snapshot is a chunked `pg_dump` (`state_sync.go:createSnapshot` →
`createPgDump`) restricted to a **33-table allowlist** of core consensus tables.
It contains no application tables and no `etl_*` tables.

**That is correct for this fleet.** No node runs the ETL today, and the API is
intended to skip to the tip — which lines up with where the old-chain indexer
left off. Nodes need consensus state, not the migrated application data, so the
allowlist needs no change and this is not a blocker.

### Restoring a snapshot truncates the entire public schema

The data directory is namespaced by chain ID, but **Postgres is not**. There is
one `OPENAUDIO_DB_URL`, and both core (`config.go:260`) and mediorum
(`mediorum/mediorum.go:162`) read it, so every table lives in one `public`
schema shared by both chains.

Before loading a snapshot the restore runs, for **every** table in `public` —
not just the 33 it restores (`state_sync.go:881-898`):

    SELECT tablename FROM pg_tables WHERE schemaname='public'
    -- then TRUNCATE TABLE <each> CASCADE

So a node joining the new chain has its entire database cleared and then
repopulated with only the core consensus tables. Per table group:

| Tables | After state sync |
|---|---|
| `core_*` (the 33 in the allowlist) | wiped, restored from the snapshot — the intended outcome |
| `etl_*` and app tables | wiped, not restored — fine, nothing runs the ETL (§2) |
| mediorum `blobs` | wiped, rebuilt by re-listing the bucket. No blob re-download, but a full list cycle |
| `delist_statuses` + `delist_status_cursor` | both wiped together, so the poller re-fetches from the trusted notifier from zero and self-heals |

The delist behaviour is worth being precise about: it self-heals **only because
the cursor is truncated alongside the data** (`delist_statuses.go:175`). Were
the data cleared and the cursor kept, the poller would resume from the old
cursor and the gap would be permanent. Nothing needs carrying across by hand.

### First snapshot arrives at height 100,000

`createSnapshot` only fires on `height % BlockInterval == 0`, and the catch-up
path computes `latestHeight - (latestHeight % blockInterval)`
(`state_sync.go:187`). The chain starts life at 5,819, so with the default
interval of 100,000 there is **no snapshot until the chain reaches 100,000** —
94,181 blocks away.

At the prod cadence of one empty block per second that is **~26 hours**, and
~37 hours if blocks average 1.4s. Roughly a day to a day and a half.

Letting it churn is the right call: it needs no config change, no code change,
and no deviation from how every other node is configured, and it buys a natural
soak window to prove out dual-writing before anyone else can join. The tradeoff
to accept knowingly: until that snapshot exists, any node joining the new chain
must block-sync all 5,818 migration blocks and re-execute them. So **do not
point other nodes at the new chain before height 100,000**.

If that window ever needs shortening, `OPENAUDIO_STATE_SYNC_BLOCK_INTERVAL`
lowers it — but see §7 for why not to set that on the old chain today.

## 3. The two keys — they are different, and only one is ephemeral

This distinction caused confusion; both keys are called "the signer" in
conversation.

### Migration transaction signer — ephemeral by design, keep it that way

`genesis_migration_address` (`0x7D01Cd0A89cc73F5a6DBEd10992AA472A2312D5F`) is
written into genesis app_state alongside `genesis_migration_end_height` (5,818)
by `cmt_state.go:155`. It signs the migration `ManageEntity` transactions and is
locked out above that height.

Nothing in this analysis discourages making it ephemeral. Destroying it after
the write is good practice — genesis pins both the address and the height ceiling,
so a leaked key buys an attacker nothing.

### CometBFT validator key — must survive the write

Separate key, different job. The writer loads `priv_validator_key.json`
(`cmt_state.go:45-47`) and signs a **real precommit vote over every block**
(`writer.go:790`), producing a `Commit` with one signature from that validator
(`writer.go:794-803`). Genesis lists that same key as the sole validator at
power 100, so the artifact is internally consistent and block-syncs verify.

The consequence: at height 5,818 the validator set is `{V}` with 100% voting
power, and validator set changes only happen through the app, which requires
blocks. **Whoever runs the bootstrap node must hold V's private key, or the
chain cannot produce block 5,819 and is dead on arrival.**

This is why the dev chain produced blocks fine: there the writer output *was*
the node's data dir, so the matching key was sitting right next to genesis. The
verification node on the T7 has a `priv_validator_key.json` whose address
(`16929B36…`) does not match its genesis validator (`2B500DE8…`), which is
exactly why it never produced a block and sat at height 0.

V can still be *ephemeral in the useful sense*: it produces blocks until real
validators register through the normal flow, then gets rotated out of the
validator set. It just cannot be destroyed at write time.

### V is not free to choose — it is derived from the delegate key

`ensurePrivValidator` (`config/setup.go:231`) derives the CometBFT key from
`OPENAUDIO_DELEGATE_PRIVATE_KEY`:

- file missing → generated from the derived key
- present and matching → loaded
- present and **not** matching → regenerated, unless it has prior signing
  history (`LastSignState.Height > 0`), in which case the node refuses to start
  rather than risk a double-sign

So the genesis validator cannot be an arbitrary key kept in a vault. It must be
the key rickyrombo will derive from its own delegate key, or the node comes up
as a non-validator and the chain is dead. This also resolves the custody
question: the writer does not need a key handed to it out of band, it needs the
bootstrap node's delegate key.

Three files are easy to conflate:

| File | Purpose | Shared when seeding a second node? |
|---|---|---|
| `config/node_key.json` | P2P identity — the ID in `<id>@host:26656` | No — must be unique |
| `config/priv_validator_key.json` | ed25519 consensus key, signs precommits | No — derived per node |
| `data/priv_validator_state.json` | last signed height/round, double-sign guard | No |

A node whose validator key is not in genesis `validators[]` is simply a full
node: it follows and validates but never proposes or votes. That is the desired
state for every node except the bootstrap.

## 4. What the genesis writer needs to be shippable

Less than it might appear.

**No code change is strictly required.** `--priv-validator-key-file`
(`main.go:101`) already lets you point the writer at the real bootstrap node's
validator key, so genesis lists that node and it can produce blocks immediately.
The operational cost is that the writer machine temporarily holds that key.

**The bootstrap *peer* list is not a writer concern.** The writer produces the
*validator* set (genesis); the *peer* list is `ProdPersistentPeers` in
`config.go:55`. Two different lists in two different places — no writer change
needed for peers.

Optional, worth considering:

- **Refuse to run when genesis and the key disagree** — see below.

### Why genesis has one validator, not nine like prod

Mirroring prod's shape (9 validators at power 10) produces a chain that cannot
commit a single block. Two independent CometBFT constraints:

1. **Signature slots must equal validator-set size.**
   `verifyBasicValsAndCommit` (`cometbft/types/validation.go`) rejects on
   `vals.Size() != len(commit.Signatures)`. The writer emits a **one-element**
   `Signatures` slice (`writer.go:798`), so with nine validators every block
   fails validation outright.
2. **The signer needs >2/3 of total voting power.**
   `votingPowerNeeded = TotalVotingPower * 2 / 3`. At 9 × power 10, one
   validator holds 11% — not enough to sign history, and not enough for the one
   node actually running to produce block 5,819 either. The chain halts at the
   migration boundary.

Hence one validator at power 100: 100% of voting power is the only shape where
a single signer can both write the history and continue the chain.

Pre-listing the other validators would require *both* giving bootstrap >2/3
(e.g. bootstrap 100, others 1 each → 91.7%) *and* changing the writer to pad
`Signatures` with `BlockIDFlagAbsent` entries. Not worth it: at power 1 the
others are decorative, so the validator-set rebalance still has to happen
through the app afterward. Add real validators post-launch via the normal
registration flow instead.

This does leave the bootstrap node a single point of failure until real
validators are registered and weighted — but that is true of any shape where
only one node is actually running, so it is not a cost of this choice.
- **Refuse to run when genesis and the key disagree.** `ensureGenesisFiles`
  returns early when both files exist without checking they match, which is how
  the T7 artifact ended up with a mismatched pair. A one-line comparison would
  turn a silent dead chain into a startup error.

## 5. Bootstrap node: audius.rickyrombo.com

### Will it try to connect to the old network?

It will **dial** old-network hosts — `ProdPersistentPeers` is baked in and
retried every 15s (`setup.go:181`) — but every connection is **rejected at the
handshake** on the `NodeInfo.Network` mismatch. No blocks, no state, negligible
bandwidth. What you get is dial churn and noisy logs in both directions, since
old nodes have this host in their address books too.

Suppress it on this box with `OPENAUDIO_PERSISTENT_PEERS` — env var, no release
needed. The writer output's `addrbook.json` is empty, so nothing else seeds old
peers.

### Do the bootstrap lists need code changes?

**Yes, two**: `ProdPersistentPeers` and `ProdStateSyncRpcs`. On this box both can
be overridden by env var, but the fleet only consumes the container, so the
constants must change in the shipped image. Note `audius.rickyrombo.com` is
**not** in `ProdStateSyncRpcs` today — that list is `creatornode.audius.co`,
`rpc.audius.co`, `v.monophonic.digital`.

### Fresh DB directory + new chain artifacts — what breaks?

- **Chain data**: nothing to do. `RootDir/<chainID>` puts the new chain in a new
  directory beside the old one. Keep the old directory until rollback is off the
  table.
- **Node identity**: keep the existing `node_key.json`, since its ID is what goes
  into `ProdPersistentPeers`. A regenerated one invalidates the entry you shipped.
- **Validator key**: must match genesis (§3), or no blocks.
- **Mediorum**: safe — presence comes from the bucket, not the DB, so a cleared
  `blobs` table costs a re-list rather than re-downloads. Delist state rebuilds
  itself from the trusted notifier (§2); nothing needs carrying across by hand.

### Config for full history and serving snapshots

```
OPENAUDIO_ARCHIVE=true                       # no pruning, keep every block
OPENAUDIO_STATE_SYNC_SERVE_SNAPSHOTS=true    # default is false
OPENAUDIO_STATE_SYNC_ENABLE=false            # this node is the source
```

Leave `BlockInterval` at its default so the first snapshot lands at 100,000
(§2). Confirm free disk first: `createSnapshot` silently skips when free space
is below `SnapshotMinFreeBytes`, default **80 GiB** (`config.go:48`).

## 6. The rest of the fleet

Ordering lives in the Runbook (steps 11–13). The constraints behind it:

- **Publishing the image is the migration.** Genesis is compiled in, so every
  auto-updating node switches as soon as the tag moves. There is no separate
  "enable" step — hence the pinned tag through Phase B.
- **A snapshot must exist first.** A node that joins before height 100,000
  block-syncs 5,818 migration blocks instead of restoring a dump, which is the
  churn and egress spike the whole ordering exists to prevent.
- **Two snapshot servers, not one.** CometBFT rejects `len(RPCServers) < 2`, and
  the light client needs a witness besides the primary, so two must be
  *responding* — not merely configured.
- **Disk carries both chains** until the old directory is removed. Tell
  operators explicitly not to delete the old one early; it is the rollback path.

## 7. Can these env vars be set on rickyrombo today?

Yes mechanically — all of them are read through `env.Get` at config load, so
they need no release. But they apply to the **old** chain today, and the effects
differ:

- `OPENAUDIO_ARCHIVE=true` — safe and reversible, but it only stops *future*
  pruning; it will not recover already-pruned history. Watch disk on a ~24M-block
  chain.
- `OPENAUDIO_STATE_SYNC_SERVE_SNAPSHOTS=true` — works, costs periodic `pg_dump`
  plus snapshot disk. Largely pointless today since this host is not in
  `ProdStateSyncRpcs`, so nobody asks it for snapshots.
- `OPENAUDIO_STATE_SYNC_BLOCK_INTERVAL=1000` — **do not set this on the old
  chain.** At ~24M blocks it triggers a full `pg_dump` every 1,000 blocks, i.e.
  roughly every 17 minutes, forever.

Recommendation: set them at new-chain cutover, not before. The only one worth
turning on early is `ARCHIVE`, and only if disk headroom is comfortable.

## 8. Relay / write cutover

Three independent switches in the api repo (`config/config.go:84-95`), and the
independence is the point:

| Flag | Effect |
|---|---|
| `newChainQueueEnabled` | relay inserts each confirmed tx into `new_chain_queue` (`api/relay.go:292`) |
| `newChainFlushEnabled` | starts the flusher goroutine that forwards queued rows to the new chain |
| `newChainFlushFromBlock` | deletes queued rows with `confirmed_block` below it, trimming what the backfill already covers |

The enqueue happens **inside the success branch, after the old chain confirms**,
and is fire-and-forget so a queue failure never fails the client's relay. The
queue is therefore strictly a mirror of what the old chain already accepted.

1. **Enable queueing early.** Costs one table insert per relayed write and
   touches the new chain not at all. Safe well before rollout.
2. **Enable flushing once the bootstrap node is healthy.** Late-joining nodes
   pick these writes up via snapshot or block sync.
3. **Stop writing to the old chain last.** See §9 — this, not the flush, is the
   irreversible step.

### Use a cursor, not delete-on-success

`flushRow` currently deletes each row after a successful forward
(`api/new_chain_flusher.go:194`). For this migration that is the wrong
semantics, because **the chain will be regenerated** with the real validator key
(§3). Any write already flushed and deleted survives only on the chain that gets
discarded.

Replacing the delete with a `last_flushed_id` cursor makes the queue a durable
log: after regenerating, set `newChainFlushFromBlock` to the new backfill's end
and re-drive the remainder. That composes with the trimming logic that already
exists, and it is only possible if the rows were never deleted. Replay is
idempotent — the same signed transactions are rejected as duplicates by a chain
that has them and accepted cleanly by a rebuilt one.

## 9. Rollback

Ship the previous image. The old chain's data directory is untouched at
`RootDir/audius-mainnet-alpha-beta`, so nodes resume where they left off.

**But that only covers the blockstore.** Postgres is shared between the chains
(§2), and joining the new chain truncates it, so a rolled-back node comes up
with the old chain's blocks on disk and the *new* chain's app state in Postgres.
CometBFT would ask the app for its height, get the new chain's, and try to
replay the old chain's blocks forward over incompatible state — an app-hash
mismatch almost immediately.

So rollback is only free for nodes that have **not yet joined** the new chain.
Once a node has state-synced, recovering the old chain means restoring its
database or re-syncing from scratch. Three consequences for the plan:

- **Take a Postgres backup on each node immediately before it switches.** That
  is what turns rollback back into a bounded operation, and it costs one dump.
- Roll out in waves. Every node that has switched is a node whose rollback now
  has a real cost, so keep the un-switched population large until confidence is
  high.
- **Hold back old-chain snapshot servers** (below). This is the cheap fix: it
  turns rollback from "restore a backup" into "state sync back".

### Keep at least two nodes on the old chain

A rolled-back node can recover by state-syncing the old chain again — the
restore truncates Postgres and repopulates it with old-chain state, which is
exactly the repair needed. That requires old-chain snapshot servers to still
exist, with two constraints:

1. **At least two of them.** CometBFT rejects `len(cfg.RPCServers) < 2` with
   `ErrNotEnoughRPCServers` (`cometbft/config/config.go:1060`). One anchor is
   not enough.
2. **They must already be in the old image's `ProdStateSyncRpcs`** —
   `creatornode.audius.co`, `rpc.audius.co`, `v.monophonic.digital` — because a
   rolled-back node runs the old binary and only looks there. Holding back
   `audius.rickyrombo.com` would not help; it is not in that list.

With four controllable nodes, both sides sit exactly at the minimum: two old
(rollback anchor), two new (`rickyrombo` plus one). There is no slack on either
side for the duration, which is worth naming as a known thin spot.

Cross-chain contamination is prevented, but by CometBFT rather than by us: the
light client verifies commits against `trustedHeader.ChainID`
(`cometbft/light/verifier.go:56`) and the chain ID is inside the vote sign
bytes, so a new-chain snapshot cannot verify on an old-chain node. Note that
`OfferSnapshot` does *not* check the `ChainID` carried in the snapshot metadata
(`abci.go`), so this protection is entirely CometBFT's — prove it in a drill
rather than assume it.

Two things to verify rather than assume: `ServeSnapshots` defaults to **false**,
so confirm it is actually on for the held-back nodes; and `Keep = 2` snapshots
at a 100,000-block interval on a ~1s chain is roughly a two-day rollback window,
beyond which recovery is a full re-sync regardless.

**Dual-writing does not compromise rollback.** Every queued transaction was
confirmed on the old chain before being enqueued, so the old chain stays
complete no matter how much has been flushed. Queueing and flushing are both
reversible.

The point of no return is when the relay **stops writing to the old chain**.
From then on the old chain is missing data and rolling back loses it. That is
the step to announce and gate — not the flush.

## 10. Open questions

- One ephemeral bootstrap validator rotated out later, or several real ones in
  genesis? The former needs no writer change (§4).
- Does the flusher move to a cursor before or after the genesis regeneration?
  Before is safer — writes flushed against the discarded chain are otherwise
  unrecoverable.
- Who runs the production genesis, on what hardware, and does the writer machine
  holding the validator key raise a custody concern worth designing around?
- Does anything besides the delist tables count as operator state that the
  migration does not reconstruct?

## 11. The API indexer is the same ETL, and that is the problem

`api/` embeds `github.com/OpenAudio/go-openaudio/pkg/etl` and runs it as its
indexer (`indexer/indexer.go:92`), so its resume semantics are the ones in this
repo.

**It cannot currently be told where to start.** `indexer.go` calls `SetConfig`,
`SetDBURL`, `SetCheckReadiness` and the hooks, but never
`SetStartingBlockHeight`, and there is no config plumbed for it. So it always
takes the resume path:

    SELECT MAX(block_height)::bigint FROM etl_blocks HAVING MAX(block_height) IS NOT NULL

That query has **no `chain_id`** — `etl_blocks` is `(id, proposer_address,
block_height, block_time)`. Pointed at the new chain it therefore resumes at the
old chain's height, roughly 24M, against a chain at ~100,000, and polls for a
block that will not exist for years. It fails silently, as a stall.

The mechanism exists upstream and takes precedence over the resume
(`indexer.go:290`):

    if e.startingBlockHeight > 0 {
        latestHeight = e.startingBlockHeight - 1   // "starting from explicit height, not resuming"
    } else {
        latestHeight, err = e.db.GetLatestIndexedBlock(...)
    }

So the change is small — expose a start height in `api/config` and pass it to
`SetStartingBlockHeight` — but it must land before the cutover.

### Why draining the queue first matters

Skipping to the new chain's tip is safe for everything *below* that point: those
blocks are either the migration backfill or writes that were relayed to the old
chain, indexed from it, and later flushed. All of it is already in the API's
database.

The hazard is above the switch point. A write still sitting in
`new_chain_queue` at the moment of the switch gets forwarded afterwards, lands
above the tip the indexer started from, and is indexed a **second** time — the
first having come from the old chain. Entity handlers are largely upserts and
survive that, but `InsertPlay` (`etl/db/writes.sql.go:110`) is a bare
`insert ... values` with no `ON CONFLICT`, so plays duplicate.

Draining the queue before switching removes the overlap entirely.
