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
