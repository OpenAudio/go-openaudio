# Replaying a genesis chain

How to index a genesis-writer chain back into an ETL database, and how to make
it finish in hours rather than days.

None of this belongs in the indexer. Every item here is an operational choice
for a one-shot bulk replay; the shipped code is unchanged by it. The single
exception is deliberate and scoped: `migrationOnlyBlock` skips the per-tx
savepoint for blocks made entirely of migration transactions, and live or
mixed blocks keep per-transaction isolation.

## Running a replay

`replay.sh` runs the whole pipeline -- bulk-load settings, index slimming, the
replay itself, and the restore -- and needs only `go` and `psql` on the host:

    ./replay.sh run \
      --rpc http://localhost:50051 \
      --db 'postgres://postgres@localhost:5433/etl?sslmode=disable' \
      --end 5800000

The phases, which the sections below explain individually:

1. Apply `bulk-load-settings.sql`. `shared_buffers` needs a Postgres restart;
   pass `--restart-cmd 'docker restart etl-pg'` (or your `pg_ctl` equivalent)
   to let the script do it, otherwise it stops and asks.
2. Bootstrap: index one block so the ETL migrations create the schema.
3. Slim: apply `drop-serving-indexes.sql` and `bulk-load-tables.sql`.
4. Replay to `--end`.
5. Restore: apply `restore-settings.sql` and `recreate-serving-indexes.sql`,
   then `VACUUM ANALYZE`.

Every phase is idempotent, and if the replay dies partway nothing is restored
on purpose -- recreating the indexes on a partially loaded database is
expensive and a resume would only drop them again. Rerun `replay.sh run` with
the same arguments to resume from the last indexed block, or run
`replay.sh restore --db ...` to give up and put the database back into
serving shape.

## The drop list is measured, and workload-specific

Every index in `drop-serving-indexes.sql` recorded zero scans across a full
58M-transaction replay (2026-08). Dropping the set was worth 1.85x on its own:
each of those indexes is maintained by every insert, and their pages evict the
hot ones from the buffer cache, turning inserts into random reads.

Two things about the list that look wrong but are not:

- `users_new_wallet_idx` and `users_new_handle_lc_idx` are dropped even though
  the live indexing path looks up users by wallet and handle. Genesis-migration
  transactions skip signer validation, so those lookups never fire on this
  workload. Do not reuse this list for a replay of live traffic.
- Primary keys and unique indexes are never dropped: the former may back
  `ON CONFLICT`, the latter are upsert arbiters.

### Re-measuring the drop list

If the indexer's queries or the schema change, re-measure rather than guess --
but do it as its own exercise, not inside a replay. After a run (or a sizable
prefix of one) *without* the drops applied, zero-scan candidates and their
exact recreate DDL come from the run's statistics:

    select i.relname, pg_get_indexdef(i.oid)
    from pg_class t
    join pg_index x on x.indrelid = t.oid
    join pg_class i on i.oid = x.indexrelid
    left join pg_stat_user_indexes s on s.indexrelid = i.oid
    where t.relname in ('etl_transactions','etl_manage_entities','etl_addresses',
                        'etl_plays','follows','saves','reposts','subscriptions',
                        'users','tracks','playlists')
      and not x.indisprimary
      and not x.indisunique
      and coalesce(s.idx_scan, 0) = 0
    order by pg_relation_size(i.oid) desc;

Then update `drop-serving-indexes.sql` and `recreate-serving-indexes.sql`
together. Sample well past the first entity type: a genesis chain replays
entity types in sequence, so an early prefix is not representative of the
whole run.

## Run the ETL on the host, not in the node container

This is the single largest factor and it is not a Postgres setting.

The indexer issues roughly nine sequential round-trips per social transaction
-- existence checks, wallet lookup, the entity write, the auto-subscription,
and three ETL audit-table writes. It pays network latency on every one, so
throughput is set by round-trip time, not by query cost.

    host -> localhost           0.072 ms per round-trip
    container -> host.docker.*  0.354 ms per round-trip   (4.9x)

    9 x 10,000 x 0.354 ms = 31.9 s/block   (observed ~29 s)
    9 x 10,000 x 0.072 ms =  6.5 s/block

Run the node in Docker if convenient -- it serves one request per *block* --
but point a host-native ETL process at it. Publish the node's port 80 and give
the indexer `SetDBURL` over loopback.

## Measured effect (2026-08 run, 58M transactions)

    embedded in node container      387 tx/s     ~30 h
    host-native ETL                 714 tx/s     1.85x
    + drop zero-scan indexes       1320 tx/s     1.85x
                                                 3.4x total

## Finding the next bottleneck

Do not guess -- two of my hypotheses were wrong (block size, log volume) and
cost real time. Sample what Postgres is actually doing:

    select coalesce(wait_event_type,'CPU')||'/'||coalesce(wait_event,'running'),
           left(regexp_replace(query,'\s+',' ','g'), 60)
    from pg_stat_activity
    where datname = current_database() and state = 'active'
      and pid <> pg_backend_pid();

Read it as:

    Client/ClientRead dominant   Postgres is idle waiting on the client.
                                 The bottleneck is round-trip latency or the
                                 indexer itself -- not the database.
    IO/DataFileRead dominant     Index pages are missing the buffer cache.
                                 Drop unused indexes; raise shared_buffers.
    CPU/running dominant         Genuine query work. You are near the floor
                                 for this shape of workload.

Filter out the node's own connections (`pg_database_size`, `RegisteredNodes`,
`ValidatorFromPeer`) or they will dominate the sample and mislead you.

## What did not work

- **Block size.** Blocks hold 10,000 transactions and the indexer opens a
  savepoint per transaction, so each block ran 10,000 subtransactions deep --
  far past Postgres's 64-subxid cache. Measured across a 400x range (25 to
  10,000 per block) the difference was inside noise. Subtransaction SLRU
  lookups scale with transaction count, not nesting depth. `--max-txs-per-block`
  is not a performance lever.
- **Log level.** Every indexed entity logs an info line, ~58M of them. Setting
  `OPENAUDIO_LOG_LEVEL=warn` changed throughput by nothing measurable. Bound
  the Docker log (`--log-opt max-size`) so it does not reach 14 GB, but do not
  expect speed from it.
