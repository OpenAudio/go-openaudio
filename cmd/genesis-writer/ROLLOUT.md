# Genesis migration rollout

Draft. The runbook is the plan; everything under Reference is the why, cited to
code. Open questions are at the end and are genuinely open.

## TL;DR

Move the network from `audius-mainnet-alpha-beta` to `audius-mainnet-beta`: a new
chain seeded with prod state, stood up on two nodes, joined by the fleet in
batches, then cut over.

| | | |
|---|---|---|
| **A** | 1 | Generate + verify the genesis artifacts |
| | 2 | Register `v.audius.rickyrombo.com` (second state-sync RPC) |
| | 3 | Build the new binary — do not merge and deploy |
| **B** | 4 | Run the bootstrap node on the new chain |
| | 5 | Route plays through old-chain hosts |
| | 6 | Swap `audius.rickyrombo.com` to the new node |
| | 7 | Stand up the second snapshot RPC |
| | 8–9 | Confirm blocks, then snapshots |
| **C** | 10 | Migrate the fleet — **≤10 nodes per jail cycle** |
| | 11 | Enable flushing |
| | 12 | Switch the indexer at height `L` |
| | 13 | Revert play routing |
| | 14 | **Point of no return** — stop relaying to the old chain |
| | 15 | **Irreversible** — roll `:stable`, retiring the rollback anchors |
| | 16 | Retire `v.audius.rickyrombo.com` |

**The three that bite.** Step 10: a departed validator keeps its voting power
until jailed, and past ⅓ the old chain halts with no self-recovery. Step 12: `L`
must be a height in the *future*, or the indexer sails past it and blocks get
indexed twice. Step 5/13: plays never touch the relay queue, so without routing
they split across both chains for days.

**Before starting:** merge api#1018 (or the first flush deletes rows off a chain
that gets regenerated), api#1028 (step 12 cannot run without it), api#1029
(step 5), and #551 (or every state-syncing node loses its mediorum tables).
The binary for steps 4-10 is #553, which must **not** be merged -- run the image
CI builds from it.

---

# Runbook

Ordered. Steps 1-13 are reversible, at costs ranging from restarting a container
to re-syncing a node. **Step 14 is the point of no return** -- after it the old
chain is missing writes. Step 15 retires the rollback anchors, and step 16 is
cleanup.

## Phase A -- build the shippable chain

**1. Generate the genesis artifacts.**

1. Genesis writer runs using `audius.rickyrombo.com` as the genesis bootstrap set.
2. Verify them using the parity checker, consulting the known parity
   differences below.

> **Getting a source snapshot first.** The writer reads a restored Discovery
> Provider database, and the routine backup is not directly restorable: the
> `db-backup` job in audius-k8s builds its `pg_dump` from a `--table` allowlist,
> which omits types and functions, so the restore fails on missing types rather
> than on anything obvious. Take the dump with `--exclude-table-data` instead, so
> the schema comes across whole and only the bulk rows are skipped. Verify this
> against the current job before relying on it.
>
> `user_balance_history` is roughly 85 GB of a ~276 GB snapshot and the migration
> never reads it. Excluding its data cuts the restore substantially. Confirm
> nothing you need has started reading it before dropping it.
>
> **Running the verification.** Serve the artifact from a node, replay it into a
> scratch ETL database, then compare that against the source snapshot. Point the
> replay at the **serve copy**, never the sealed original, and point `--db` at an
> ETL database, never the chain database -- the replay creates ETL schema and
> drops serving indexes in whatever it is given.
>
> ```
> cmd/genesis-writer/replay/replay.sh run \
>   --rpc http://localhost:50051 \
>   --db  postgres://.../etl_<name> \
>   --end <writer end height> \
>   --restart-cmd '<how to restart postgres>'
>
> cd pkg/etl && go run ./parity \
>   --db      postgres://.../etl_<name> \
>   --prod-db postgres://.../<source snapshot>
> ```
>
> `replay.sh run` is idempotent and resumes, so a failure part-way can just be
> re-run. It ends by restoring settings, recreating the serving indexes it
> dropped, and vacuuming; parity needs that to have finished.
>
> **Calibration, from the 2026-08-25 verification run** (`/Volumes/T7 Shield/
> genesis-writer-test-6-30/run-2026-08-25`, against a 276 GB prod snapshot):
> writer 3h47m producing 5,839 blocks and 58,235,010 transactions; replay 10h00m
> indexing 56,817,444 manage-entity rows and 28,341,822 plays with 206 rejections;
> parity ~4 minutes over 410,043 sampled rows. Two runs from the same snapshot
> produced identical counts, so a deviation is signal. Use these to sanity-check
> a run in progress, not as expected values -- a fresh snapshot changes all of
> them.

> *The source DSN and the validator key are not sufficient inputs.* `rewards` is
> the writer's **first** step, so anything missing here fails the run in its
> first second rather than hours in:
>
> - `--core-dsn` -> the **old core chain** Postgres. Reward pools and rewards are
>   rebuilt from `core_reward_pools` / `core_rewards`. Read-only, safe against a
>   running node, but it must be reachable for the whole run -- over a
>   port-forward, that means the forward stays up.
> - `LAUNCHPAD_OLD_SECRET` and `LAUNCHPAD_NEW_SECRET` in the environment -- env
>   only, never flags, since a flag reaches `argv`.
> - `--launchpad-mints` -> the mint address file. Every launchpad key is a
>   function of `(secret, mint)`; the secrets alone derive nothing.
> - The destination database must already **exist**. `--run-migrations` applies
>   the schema but does not create the database.
> - The destination must be empty in **both** halves: an empty Postgres database
>   *and* no leftover CometBFT `core/` directory. A stale blockstore panics the
>   writer immediately with `BlockStore can only save contiguous blocks`.
>
> Budget the runtime from a measured run rather than this document, and re-derive
> it before it is used to size the cutover window.

> **Seal the artifact before pointing anything at it.** The verification node,
> the ETL, and every other service take the chain DB as `OPENAUDIO_DB_URL`, and
> any of them can destroy it. A node whose binary does not embed *this*
> artifact's genesis does not fail -- it takes its "generate a new genesis" path
> and runs the core migrations **down**, dropping every table the writer just
> filled. `OPENAUDIO_ENV=dev` loads the embedded `dev.json`, so a binary built
> before the genesis was copied in looks completely normal until it wipes the
> write. This has happened once already, on 2026-08-25.
>
> ```sql
> ALTER DATABASE <artifact> WITH ALLOW_CONNECTIONS false;
> CREATE DATABASE <artifact>_serve TEMPLATE <artifact>;
> ```
>
> A sealed database still works as a `TEMPLATE` -- that is how `template0`
> works -- so sealing first leaves no window where it is reachable, and the copy
> is file-level (minutes, not a re-run). Point the node, the replay, and parity
> at `<artifact>_serve`. Verify the artifact **after** the services are up: a
> count taken before you start a node certifies a state the node may then change.

> **Parity does not come back clean, and it is not supposed to.** Treat these as
> expected and investigate only what is *not* on this list:
>
> | Divergence | Why it is expected |
> |---|---|
> | `playlists.metadata_multihash` | chain provenance the migration legitimately rewrites |
> | `track_price_history`, `album_price_history` | derived from current metadata, so history collapses to one row per entity. Harmless in production: the API keeps its own database and skips to the tip (Reference SS2), so it never reads these from a from-genesis replay |
> | `in_playlists_entries` | the source holds duplicate array entries; the ETL total equals the source **distinct** count |
> | `playlists_previously_containing_track` | the migration is *more* complete than the source |
> | `stems`, `remixes` | ditto -- it recovers relationships the legacy Python indexer never wrote, derived from each track's own `stem_of` / `remix_of` |
>
> The last three are cases where the reference is the less accurate side. A
> review that assumes "source is truth" will flag the migration for being right.
>
> Row-level mismatches you will also see, all pre-existing ETL behaviour rather
> than migration defects -- they would occur identically on live indexing:
>
> | Mismatch | Cause |
> |---|---|
> | a handful of `tracks.genre`, e.g. `sn_SomeGenre` -> `Sn_somegenre` | `NormalizeGenre` re-cases on write, while `ValidateGenre` documents genre as free-form. The two disagree; the chain carries the original, so re-indexing fixes it |
> | occasional `tracks.musical_key` -> NULL, e.g. `"G flat"` | the allowlist mirrors apps' `is_valid_musical_key` and drops anything outside it. The chain carries the value |
> | `track_downloads` reports rows "missing" | not missing -- row counts match exactly. The migration disagrees on `parent_track_id` for downloads of tracks that have since been **deleted**: deletion clears `stem_of` and the `stems` row, so the ETL cannot recover the historical parent. Every consumer joins on the downloaded track and filters `is_delete = false`, so those rows are already excluded from both counts. Nothing reads them |
>
> Every "missing" row in the report should reconcile to one of these or to the
> stems/remixes and price-history entries above. On the 2026-08-25 run all 2,980
> did. An unexplained one is worth stopping for.

**2. Register a new validator to be the other state-sync RPC for the new network.**

1. The new network needs two state-sync RPCs. `creatornode.audius.co` and
   `v.monophonic.digital` are that for the current network, so they cannot move.
2. Instead, rickyrombo registers a new validator to serve as the second
   state-sync RPC. Call it `v.audius.rickyrombo.com` for now (endpoint TBD).

**3. Create a new binary for the new network. Do not merge and deploy.**

**#553** is this branch. Merging it and shipping it as `stable` *is* the mass
migration, so it stays open; what you want is the image CI publishes from it,
tagged by commit sha (`openaudio/go-openaudio:<sha>`, multi-arch).

1. Update `pkg/core/config/genesis/prod.json` to the new genesis file.
2. Update `ProdPersistentPeers` to the two bootstrap nodes
   (`v.audius.rickyrombo.com` and `audius.rickyrombo.com`).
3. Update `ProdStateSyncRpcs` to the same two.

> The node key **is** the comet key (`p2p.NodeKey{PrivKey: envConfig.CometKey}`,
> `setup.go:105`), so a node's P2P id is its validator address. The bootstrap's
> id is therefore its genesis validator address lowercased, derivable before it
> ever runs. `v.audius.rickyrombo.com` cannot be listed until it exists; until
> then the second node reaches the network by dialling the first and PEX
> propagates. Fill it in before step 15.

## Phase B -- stand up the new network

**4. Run a production node with a different config and genesis but the same key
as `audius.rickyrombo.com`.**

1. Spin up another node with the new embedded genesis file, preseeded with the
   core files (using a different network name and location) and a fresh database.
   **Exclude `node_key.json`.** The one in the writer output belongs to whatever
   node last ran against that directory, and seeding it gives the bootstrap a
   P2P identity unrelated to its delegate key -- and unrelated to the id in
   `ProdPersistentPeers`. Delete it and let the node derive its own.
2. Point its storage at the same storage as the old node so both have all the
   blobs.
3. Environment:
   1. `OPENAUDIO_ARCHIVE=true` -- keep the whole chain history.
   2. `OPENAUDIO_STATE_SYNC_SERVE_SNAPSHOTS=true` -- take snapshots.
   3. `OPENAUDIO_STATE_SYNC_ENABLE=false` -- it is the source, not a consumer.
   4. `OPENAUDIO_PERSISTENT_PEERS` overridden to an empty list -- **not** to
      escape old-network peers. Step 3 already replaced `ProdPersistentPeers` in
      this binary, so the baked list is the two new bootstrap nodes. The reason
      is narrower: at this point only this node exists, so the list resolves to
      itself plus `v.audius.rickyrombo.com`, which is not up until step 7. The
      override just avoids dial churn against a host that does not exist yet,
      and can be dropped once step 7 is running.
   5. `BlockInterval` set to 20,000, `Keep` left at 2. Short enough that the
      first snapshot does not take a day, long enough that a syncing node does
      not have its snapshot rotate out from under it, and `Keep=2` holds storage
      down.
4. Confirm free disk for snapshots (**80 GiB** -- below `SnapshotMinFreeBytes`
   they silently skip).

**5. Deploy an API change so all plays go through `v.monophonic.digital` and
`creatornode.audius.co` temporarily.**

1. api#1029 adds `playRoutingHosts` for this. It keeps plays from being
   segmented across both networks as nodes split.

> Plays are recorded by whichever node **serves the audio** -- `logTrackListen`
> runs at the top of mediorum's `serveBlob`, before it 307s to storage -- and
> they never pass through the relay, so `new_chain_queue` does not carry them.
> A migrated node therefore writes its plays to the new chain while the indexer
> is still reading the old one, and nobody indexes them. Over a multi-day fleet
> migration that is a large fraction of all plays.
>
> Point the routing at nodes that stay on the old chain. `creatornode.audius.co`
> and `v.monophonic.digital` are the natural choice: store-all, and held back as
> rollback anchors anyway. Bandwidth is not a concern -- they 307 to presigned
> storage rather than streaming bytes, so the cost is one request and a signature
> per play. The original hosts stay behind them as fallbacks, because a freshly
> uploaded track may not have replicated to a store-all node yet and must still
> stream.
   Plays are submitted by whichever node serves the audio, never through the
   relay, so without this every already-migrated node writes its plays to the
   new chain while the indexer is still reading the old one.

**6. Swap the old node for the new one.**

1. Switch the Caddy proxy to point `audius.rickyrombo.com` at the new-network
   validator container.
2. Bring down the old node.
3. Bring up the new node with the peering port open.

**7. Spin up the other snapshot RPC (`v.audius.rickyrombo.com`).**

1. Same genesis artifacts, database, and environment as step 4, except its own
   delegate wallet.
2. Disable storage -- avoids mediorum churn, and storage proofs are irrelevant
   for it.
3. Confirm free disk for snapshots (**80 GiB**).

**8. Confirm blocks are being produced.**

1. Check the writer's end height in `BUILT_FROM.txt` and confirm the node
   produces the block after it. Do **not** hardcode a height here -- each run
   against fresh prod data ends somewhere different, and a stale number turns a
   healthy chain into an apparent failure. If it does not produce that block,
   the validator key does not match genesis: the dead-chain failure mode, which
   looks like "waiting for comet to catch up" with height stuck at 0.

**9. Confirm snapshots get created.**

## Phase C -- cut over

**10. Bring other nodes to the new network manually, by having operators set an
explicit tag.**

1. Target friendlies with small fleets first, for redundancy before flushing
   starts.
2. Move **at most 10 at a time**, and wait for them to be jailed on the old
   network before the next 10. A departed validator keeps its voting power until
   jailed, and past one third of total power the old chain halts -- taking the
   rollback anchor with it.
3. **Do not** migrate the old network's snapshot RPCs. They are the rollback plan.
   Land #551 before this step, or every node that state syncs loses its mediorum
   tables and the whole fleet regenerates previews and analyses at once.
4. Ideally make one of the migrated nodes a state-sync RPC so the pressure is not
   all on one machine.

> **Why ten, and why wait.** Every registered node runs its own warden on a
> 60-minute ticker (`registry_bridge.go:104`), and each run jails at most one
> validator, so fleet-wide throughput is high -- not the constraint. The
> constraint is eligibility: a validator must propose **no blocks across 8 SLA
> rollups**, and a rollup is 2048 blocks (~34 minutes at prod's 1s cadence), so
> roughly **4.5 hours** of silence before it can be jailed at all. A batch
> therefore clears in about five hours, not one. Departed-but-unjailed
> validators keep their voting power the whole time, which is what the ⅓ ceiling
> is measured against.
>
> Attestations are not the binding constraint and can be ignored when sizing
> batches: deregistration needs 5 signatures from a rendezvous of 15, which
> survives until roughly ⅔ of the pool is unreachable -- twice the ⅓ at which
> the chain has already halted.
>
> Jailing stops once the old network is down to ~30 active validators: the
> underperformance purge has a killswitch below that count, so nothing further
> is ever jailed. From there, roughly ten more departures halt the old chain,
> and a halted chain cannot commit the jailing transactions that would unhalt
> it -- recovery means operators bringing nodes back. So the last stretch of the
> fleet is a decision to let the old chain go, and belongs after step 14, not
> inside this step.

**11. Flush the API's queue.**

1. api#1018 must be merged first, or the flusher deletes each row after
   forwarding -- and the chain is regenerated with the real validator key before
   it ships, so anything flushed-and-deleted survives only on the discarded one.
2. `newChainFlushEnabled=true`, `NewChainURL` -> bootstrap, and
   `NewChainFlushFromBlock` set to the latest block in the snapshot used to
   generate the genesis.

**12. Switch the indexer to the new chain.**

api#1028 provides the three controls this step needs -- `etlStartingBlockHeight`,
`etlEndingBlockHeight`, and the `newChainFlushToBlock` ceiling. Without them the
step cannot be executed at all.

1. Set a height `L` in the future that the flusher will filter below and the
   old-network indexer will stop indexing at. `L` must be in the future: picking
   one at or below the current tip races the config rollout, and blocks the
   indexer sails past get indexed from the old chain *and* flushed to the new
   one.
2. After reaching `L` in indexing and finishing flushing, record the new chain's
   height `H`. "Finishing flushing" means pending rows at or below `L` reach
   zero **and stay zero** across a settle window -- the enqueue is
   fire-and-forget, so a straggler can appear after the queue first reads empty.
3. Start the indexer without a ceiling on the new network at height `H + 1`.
4. Turn flushing back on without a filter.
5. **Clear both indexer bounds.** Neither is consumed -- the ETL re-reads them
   on every startup. A start height left set makes each restart re-index from
   that height, and `etl_plays` has no unique key, so plays duplicate rather
   than upsert. An end height left set stops the indexer at `L` forever. Both
   log at warn while they are in effect.

> The filter is a *filter*, not a halt: skip rows above `L`, keep draining those
> below. Enqueue is dispatched asynchronously, so `confirmed_block` is only
> roughly ordered by `id`, and stopping at the first row above `L` would strand
> one below it. `L` is inclusive -- the block is committed before the indexer
> stops -- so the ceiling is `confirmed_block <= L`.

**13. Revert the change that routes all streams through the old network**
(clear `playRoutingHosts`, api#1029).

1. This needs to happen immediately after step 12, or before it. Ideally at
   exactly `L`, but that is a lot of coupling for the benefit.
2. Either side of `L` loses a small number of plays; neither duplicates them.
   Reverting **after** `L` leaves plays going to the old chain, which the
   indexer has stopped reading. Reverting **before** puts them on the new chain
   below `H`, which the new indexer skips. Plays have no unique key, so there is
   no dedupe path that would turn this into duplication instead.
3. Prefer **after**, because the window is then bounded by something you
   control: the drain between `L` and recording `H`. Minimize it by reverting as
   close to `L` as practical.

**14. Irreversible: stop relaying to the old network.**

1. Roll out a change making the new network the primary relay path and removing
   the queue infrastructure.

**15. Irreversible: roll out the new binary to the rest of the network as
`:stable`,** including the old network's snapshot RPCs.

**16. Retire `v.audius.rickyrombo.com`.**

1. Ship a change making another node a state-sync RPC in its place -- ideally
   `creatornode.audius.co` or `v.monophonic.digital`, now that they have moved.
   CometBFT refuses to state sync from fewer than two servers, so this must ship
   before the deregistration or new nodes cannot join.
2. Deregister `v.audius.rickyrombo.com`.

---

# Reference

## 1. Facts that shape the plan

| Fact | Where | Consequence |
|---|---|---|
| Genesis is compiled into the binary | `config/genesis/genesis.go` (`//go:embed prod.json`) | Switching chains **requires a release**. Binary ships before anything cuts over. |
| Chain ID changes: `audius-mainnet-alpha-beta` → `audius-mainnet-beta` | `genesis/prod.json:3` | Hard network split. CometBFT compares `NodeInfo.Network` at handshake, so old and new nodes **cannot** peer even when they dial each other. |
| Data dir is namespaced by chain ID | `config/setup.go:67` — `cometRootDir = RootDir/chainID` | New chain gets a **fresh directory**; old chain data is untouched. Rollback is "ship the old image". Cost: both chains on disk. |
| Prod **genesis** lists 9 validators @ power 10; new genesis lists 1 @ power 100 | `prod.json`, new `genesis.json` | One validator is 100% of voting power, so the bootstrap node commits blocks alone. See §3. |
| The **live** validator set is far larger than genesis — 67 of 72 registered nodes at the time of writing | `/core/nodes/verbose` on any node | Validators join by registration, not by genesis. Quorum and jailing math in the Runbook is over the live set, not the 9. Read the current number rather than assuming. |
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
| mediorum `uploads` | wiped, refills over HTTP via `scrollUploadsFromPeers` (`upload_client.go:58`), cursor-tracked per peer. Independent of the chain, so it heals across the migration split |
| `audio_previews` | wiped, **recomputed from audio** — `findMissedJobs` → `generateAudioPreviewForUpload` once `uploads` is back |
| `qm_audio_analyses` | wiped, **recomputed on demand** (`serve_blob.go:732`) |
| `delist_statuses` + `delist_status_cursor` | both wiped together, so the poller re-fetches from the trusted notifier from zero and self-heals |

Nothing is permanently lost, but the last two are real CPU on every node that
joins — and in a fleet-wide migration they all pay it at once. Either have
operators dump and restore those tables across the switch, or land the scoping
fix so state sync stops truncating tables no snapshot restores
(OpenAudio/go-openaudio#551), after which this row of the table becomes moot.

The delist behaviour is worth being precise about: it self-heals **only because
the cursor is truncated alongside the data** (`delist_statuses.go:175`). Were
the data cleared and the cursor kept, the poller would resume from the old
cursor and the gap would be permanent. Nothing needs carrying across by hand.

### First snapshot arrives at height 100,000

`createSnapshot` only fires on `height % BlockInterval == 0`, and the catch-up
path computes `latestHeight - (latestHeight % blockInterval)`
(`state_sync.go:187`). The chain starts life at the writer's end height — a few
thousand blocks, not zero — so with the default
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
chain cannot produce its first live block and is dead on arrival.**

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
   node actually running to produce the first live block either. The chain halts at the
   migration boundary.

Hence one validator at power 100: 100% of voting power is the only shape where
a single signer can both write the history and continue the chain.

That power does not persist once the node runs. `core_registered_nodes` starts
empty on the new chain — the writer never seeds it — so the bootstrap node finds
itself absent and self-registers through the registry bridge, which is permitted
without peers precisely because it is in genesis (`registry_bridge.go:325`).
Registration carries `ValidatorVotingPower`, **10** on mainnet
(`config.go:90`), and registrations are rejected outright at any other value
(`registration.go:52`). So V is rewritten from 100 to 10 by its own
registration. Harmless while it is alone — it is still 100% of the set — and it
is in fact how the chain reaches the normal 10-per-validator shape as others
join. Worth knowing, because the "power 100" invariant above holds only until
the bootstrap node's first successful registration.

Pre-listing the other validators would require *both* giving bootstrap >2/3
(e.g. bootstrap 100, others 1 each → 91.7%) *and* changing the writer to pad
`Signatures` with `BlockIDFlagAbsent` entries. Not worth it: at power 1 the
others are decorative, so the validator-set rebalance still has to happen
through the app afterward. Add real validators post-launch via the normal
registration flow instead.

This does leave the bootstrap node a single point of failure until real
validators are registered and weighted — but that is true of any shape where
only one node is actually running, so it is not a cost of this choice.
### Do not refuse to start when the key is absent from genesis

An earlier draft of this document suggested exactly that -- compare the
validator key against the genesis set at startup and refuse on a mismatch. It
would break the entire rollout.

New-chain genesis lists **one** validator. Every other node's key is
deliberately not in it; they join through registration (Runbook step 10), which
is the only mechanism by which the fleet ever reaches the new chain. A node that
refused to start because its key is absent from genesis would mean no node could
join at all.

The check that matters already exists and is a different one:
`ensurePrivValidator` (`config/setup.go:232`) compares the key file against the
key derived from the delegate key, and refuses to start on a mismatch **only
when there is prior signing history** -- the double-sign guard. Genesis is not
and should not be part of that comparison. (There is no `ensureGenesisFiles`
function; the genesis-file handling is `setup.go:114-126`, which writes the file
when absent and otherwise logs "Found genesis file". It does not compare
anything, and that is correct.)

The T7 case that motivated the suggestion was not a bug. A node whose key is not
in genesis block-syncs normally and does not propose -- which is right. It looked
like a dead chain only because it was the *only* node, so nobody proposed. On a
network with peers it would sync fine.

What would have helped is a diagnostic, not a refusal: log at startup when this
node's validator key is not in the genesis set and note that it will not propose
until it registers. That makes a single-node test harness obvious immediately
and costs a joining node nothing.

## 5. Bootstrap node: audius.rickyrombo.com

### Will it try to connect to the old network?

**Outbound, no** -- provided it runs the step 3 binary. That build replaces
`ProdPersistentPeers` with the two new bootstrap nodes, so there are no
old-network hosts in the baked list to dial, and the writer output's
`addrbook.json` is empty, so nothing else seeds them. (This section previously
said otherwise and prescribed an env-var suppression; that advice assumed the
node would run the *existing* binary with only genesis swapped, which step 3 no
longer does.)

**Inbound, yes, and you cannot stop it.** Old-network nodes have this host in
their own address books and will keep dialing it, retried every 15s
(`setup.go:181`). Every one of those connections is **rejected at the handshake**
on the `NodeInfo.Network` mismatch -- no blocks, no state, negligible bandwidth.
What remains is dial churn and noisy logs on both sides until those address book
entries age out. No env var on this box affects it, because the dialing is
happening elsewhere.

`OPENAUDIO_PERSISTENT_PEERS` is still worth setting empty at step 4, but for the
narrower reason given there: the second bootstrap node does not exist yet.

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

Set `BlockInterval` to 20,000 rather than leaving the 100,000 default, and keep
`Keep` at 2 (Runbook step 4). At the default the first snapshot does not exist
until height 100,000 — the catch-up path rounds down to the interval grid, which
yields 0 below the first boundary — so nothing can state sync onto the new chain
for the better part of a day, which is also the window in which the fewest nodes
are on it.

It is a producer-side setting: the three uses are all in snapshot creation
(`state_sync.go`), and consumers discover snapshots through `ListSnapshots`, so
lowering it on the bootstrap nodes needs no agreement from anyone else. Do not
lower it so far that snapshots rotate out from under a syncing node: retention is
`Keep × BlockInterval`, and `pruneSnapshots` deletes by name order with no regard
for restores in flight. Size it against a measured restore time — the allowlist
is ~46 GB on the migrated chain, nearly all of it `core_transactions`.

Confirm free disk first: `createSnapshot` silently skips when free space is below
`SnapshotMinFreeBytes`, default **80 GiB** (`config.go:48`).

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

AudiusProject/api#1018 does this — it marks rows `flushed_at` instead of
deleting them, covering all three delete paths (forwarded, backfill-trimmed, and
corrupt), and adds a partial index so retained rows stay out of the hot path. It
is open and needs to land **before** flushing is first enabled, since queueing is
already on in production.

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

There *is* a chain-aware fallback, and it is unreachable in exactly this case.
Only when `GetLatestIndexedBlock` returns `pgx.ErrNoRows` does the indexer fall
through to `SELECT MAX(height) FROM core_indexed_blocks WHERE chain_id = $1`
(`indexer.go:302`). On any database that has indexed the old chain, `etl_blocks`
is populated, so the first query answers and the chain-aware path never runs.
That is why the failure is a silent stall rather than an obvious error: the code
that would have caught it is one branch away.

The mechanism exists upstream and takes precedence over the resume
(`indexer.go:290`):

    if e.startingBlockHeight > 0 {
        latestHeight = e.startingBlockHeight - 1   // "starting from explicit height, not resuming"
    } else {
        latestHeight, err = e.db.GetLatestIndexedBlock(...)
    }

So the change is small — expose a start height in `api/config` and pass it to
`SetStartingBlockHeight` — but it must land before the cutover. api#1028 does
this, along with the matching end height and the flusher ceiling. Note that both
bounds are one-shot: nothing consumes them, so they must be cleared afterwards
or every restart re-indexes from the start height and duplicates plays.

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
