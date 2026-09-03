package ddl

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strconv"

	_ "embed"
)

//go:embed delist_statuses.sql
var delistStatusesDDL string

//go:embed add_delist_reasons.sql
var addDelistReasonsDDL string

//go:embed drop_blobs.sql
var dropBlobs string

//go:embed clean_uploads_audio_analyses.sql
var cleanUploadsAudioAnalysesDDL string

var mediorumMigrationTable = `
	create table if not exists mediorum_migrations (
		"hash" text primary key,
		"ts" timestamp
	);
`

var qmSyncTable = `
create table if not exists qm_sync (
	"host" text primary key
);
`

func Migrate(db *sql.DB, myHost string) {
	mustExec(db, mediorumMigrationTable)

	runMigration(db, delistStatusesDDL)
	runMigration(db, addDelistReasonsDDL)

	runMigration(db, dropBlobs)

	runMigration(db, `create index if not exists uploads_ts_idx on uploads(created_at, transcoded_at)`)

	runMigration(db, `drop table if exists "Files", "ClockRecords", "Tracks", "AudiusUsers", "CNodeUsers", "SessionTokens", "ContentBlacklists", "Playlists", "SequelizeMeta", blobs, cid_lookup, cid_log cascade`)

	runMigration(db, qmSyncTable)

	runMigration(db, cleanUploadsAudioAnalysesDDL)

	// cleanup historical operation-log spam from blob not found errors
	runMigration(db, `
	delete from ops where
	data->0->>'error' like 'blob (key%NotFound%'
	`)

	// Quarantine the pre-cutover Core submission backlog once. The migration
	// hash leaves pending operations created after this run untouched.
	runMigration(db, `
	update ops
	set core_tx_status = 'legacy'
	where core_tx_status in ('pending', 'error')
	`)

	// Index for fast CID→duration lookup (presigned URL expiry)
	runMigration(db, `CREATE INDEX IF NOT EXISTS idx_uploads_transcode_cid_320 ON uploads ((transcode_results::jsonb ->> '320'))`)

	// Index for bounded StoreAll audio-analysis backlog retries.
	runMigration(db, `CREATE INDEX IF NOT EXISTS idx_uploads_audio_analysis_backlog
ON uploads (COALESCE(audio_analysis_error_count, 0), audio_analyzed_at ASC NULLS FIRST, id)
WHERE template = 'audio' AND audio_analysis_status IS DISTINCT FROM 'done'`)

	// CIDs the prune job has judged not worth chasing. Repair consults this to
	// stop re-attempting pulls forever. Local only -- deliberately not a crudr
	// model, because one node's janitorial decision must not gossip to peers.
	runMigration(db, `
	create table if not exists prune_skips (
		"cid" text primary key,
		"reason" text not null,
		"created_at" timestamptz not null default now()
	)`)

	// Prune run history. A prune can walk a multi-million-object tree or make
	// thousands of peer probes, so an operator needs to see it progressing
	// rather than waiting for a terminal log line. updated_at is the field that
	// distinguishes "working" from "wedged".
	runMigration(db, `
	create table if not exists prune_runs (
		"id" bigserial primary key,
		"task" text not null,
		"dry_run" boolean not null,
		"started_at" timestamptz not null default now(),
		"updated_at" timestamptz not null default now(),
		"finished_at" timestamptz,
		"scanned" bigint not null default 0,
		"matched" bigint not null default 0,
		"removed" bigint not null default 0,
		"skips_added" bigint not null default 0,
		"error" text not null default ''
	)`)
	runMigration(db, `create index if not exists idx_prune_runs_started_at on prune_runs(started_at desc)`)

	// Accumulated evidence that a CID is gone from the network.
	//
	// declared_at is written once and never changed: data loss is a monotonic
	// set, so a recheck that fails again must not re-report as a new loss.
	// recheck_after schedules the retry instead of expiring the record, which
	// keeps "total lost" stable while still noticing if content comes back.
	runMigration(db, `
	create table if not exists repair_data_loss (
		"cid" text primary key,
		"first_failed_at" timestamptz not null default now(),
		"last_failed_at" timestamptz not null default now(),
		"failed_cycles" int not null default 0,
		"last_attempt_exhaustive" boolean not null default false,
		"declared_at" timestamptz,
		"recheck_after" timestamptz,
		"recovered_at" timestamptz
	)`)
	runMigration(db, `create index if not exists idx_repair_data_loss_declared on repair_data_loss(declared_at) where recovered_at is null`)

	// Precomputed waveform envelopes, keyed by CID.
	//
	// Local only -- deliberately not a crudr model. Mediorum rows replicate
	// solely by riding the core chain as MediorumOperation txs, and the table
	// allowlist those txs are validated against is consulted in FinalizeBlock,
	// where result codes fold into the header. Registering this table would
	// therefore make a derived rendering hint into consensus-affecting state,
	// and commit roughly a kilobyte per track to the chain permanently. Every
	// node recomputes its own instead; the inputs are pinned so they agree.
	//
	// peaks is exactly `buckets` bytes, one uint8 per bucket, and stays null
	// until status = 'done'. version exists so an algorithm change is a
	// re-sweep rather than a table rewrite.
	runMigration(db, `
	create table if not exists waveforms (
		"cid" text primary key,
		"peaks" bytea,
		"buckets" int not null default 750,
		-- No default on purpose. Every write stamps the running version, and
		-- discovery decides an upload is outstanding by the absence of a row at
		-- that version -- so a write that forgot it would quietly make the row
		-- invisible to discovery forever. Without a default that mistake is a
		-- not-null violation instead of a silent one.
		"version" int not null,
		"sample_rate" int,
		"sample_count" bigint,
		"duration_ms" bigint,
		"status" text not null,
		"error" text not null default '',
		"error_count" int not null default 0,
		-- Which upload this blob came from, when one is known.
		--
		-- Nullable and deliberately not the key. The relationship is not 1:1:
		-- an upload yields both a 320 and, when one is selected, a preview, so
		-- two rows share an upload_id. Legacy Qm content has no upload row at
		-- all and carries null. The cid stays the key because that is what the
		-- route, the peer probe and the redirect cache all address.
		--
		-- It exists so discovery can anti-join on indexed text instead of
		-- extracting jsonb from both sides, which was the expensive part of
		-- the sweep and the reason an "unanalyzed" count was unaffordable.
		"upload_id" text,
		-- When this row's result was produced. Distinct from last_attempted_at
		-- because a done row's age is about the waveform, not about when we
		-- last touched it.
		"analyzed_at" timestamptz not null default now(),
		-- When an attempt last started. The retry sweep derives due-ness from
		-- this plus the backoff for the row's status, rather than storing a
		-- computed next-attempt time.
		--
		-- Storing the fact rather than the decision is what lets a backoff
		-- change take effect on rows already written, and it removes the need
		-- for a sentinel to mean "never again" -- terminal is a property of the
		-- status, which is where it belongs.
		--
		-- Stamped before the analysis runs, not after. next_attempt_at only
		-- moved once an attempt finished, so a row stayed selectable for the
		-- whole of its own attempt and every sweep tick re-queued it.
		"last_attempted_at" timestamptz
	)`)
	runMigration(db, `create index if not exists idx_waveforms_upload_id on waveforms (upload_id, version) where upload_id is not null`)
	// status first: the retry sweep asks for one status at a time with a range
	// on the attempt time, so equality then range is exactly this order.
	runMigration(db, `create index if not exists idx_waveforms_retry on waveforms (status, last_attempted_at) where status <> 'done'`)

	// Supports the backfill's keyset walk, which pages newest-first over
	// (created_at, id) and filters to audio. Without it the walk falls back to
	// uploads_ts_idx, whose second column is transcoded_at, and a caught-up
	// re-walk -- which reads to the end of history to prove nothing is left --
	// sorts a large slice of a wide jsonb table every time.
	runMigration(db, `create index if not exists idx_uploads_waveform_scan
on uploads (created_at desc, id desc) where template = 'audio'`)
	runMigration(db, `create index if not exists idx_waveforms_version on waveforms (version) where status = 'done'`)

	// Single-row cursor for the newest-first backfill walk over uploads.
	// Newest-first so the recent slice operators actually care about lands in
	// days rather than after a full multi-week history walk.
	// The cursor doubles as the current backfill run: where the walk has
	// reached, when this pass began, and what it has done so far. A pass is
	// what the console reports on, so the counters live beside the position
	// they describe rather than in a separate history table.
	//
	// version records which waveform version the walk was performed under. When
	// the running version differs, the cursor resets and history is walked
	// again -- that is what makes an algorithm or parameter change re-backfill.
	runMigration(db, `
	create table if not exists waveform_cursor (
		"id" int primary key default 1,
		"created_at" timestamptz,
		"upload_id" text,
		"exhausted" boolean not null default false,
		"version" int,
		"started_at" timestamptz,
		"queued" bigint not null default 0,
		"archive_skipped" bigint not null default 0,
		"updated_at" timestamptz not null default now()
	)`)

	// Position of the legacy walk. An ALTER rather than a column folded into
	// the create above, because that table has already shipped: editing the
	// create would change its hash, re-run it as a no-op against the existing
	// table, and leave this column silently absent. It must also come after
	// the create, or a fresh database alters a table that does not exist yet.
	runMigration(db, `alter table waveform_cursor add column if not exists qm_key text`)

	runVacuumFull(db)
}

func runVacuumFull(db *sql.DB) {
	if !vacuumFullEnabled() {
		log.Println("skipping mediorum vacuum full; set OPENAUDIO_MEDIORUM_VACUUM_FULL=true to run it")
		return
	}

	runMigration(db, `vacuum full`)
}

func vacuumFullEnabled() bool {
	enabled, err := strconv.ParseBool(os.Getenv("OPENAUDIO_MEDIORUM_VACUUM_FULL"))
	return err == nil && enabled
}

func runMigration(db *sql.DB, ddl string) {
	h := md5string(ddl)

	var alreadyRan bool
	db.QueryRow(`select count(*) = 1 from mediorum_migrations where hash = $1`, h).Scan(&alreadyRan)
	if alreadyRan {
		fmt.Printf("hash %s exists skipping ddl \n", h)
		return
	}

	mustExec(db, ddl)
	mustExec(db, `insert into mediorum_migrations values ($1, now()) on conflict do nothing`, h)
}

func mustExec(db *sql.DB, ddl string, va ...interface{}) {
	_, err := db.Exec(ddl, va...)
	if err != nil {
		fmt.Println(ddl)
		log.Fatal(err)
	}
}

func md5string(s string) string {
	hash := md5.Sum([]byte(s))
	return hex.EncodeToString(hash[:])
}
