package ddl

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"regexp"
	"strings"

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

	// cleanup crudr spam from blob not found errors
	runMigration(db, `
	delete from ops where
	data->0->>'error' like 'blob (key%NotFound%'
	`)

	// Index for fast CID→duration lookup (presigned URL expiry)
	runMigration(db, `CREATE INDEX IF NOT EXISTS idx_uploads_transcode_cid_320 ON uploads ((transcode_results::jsonb ->> '320'))`)

	// Index for bounded StoreAll audio-analysis backlog retries.
	runMigration(db, `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_uploads_audio_analysis_backlog
ON uploads (COALESCE(audio_analysis_error_count, 0), audio_analyzed_at ASC NULLS FIRST, id)
WHERE template = 'audio' AND audio_analysis_status IS DISTINCT FROM 'done'`)

	runMigration(db, `vacuum full`)
}

func runMigration(db *sql.DB, ddl string) {
	h := md5string(ddl)

	var alreadyRan bool
	db.QueryRow(`select count(*) = 1 from mediorum_migrations where hash = $1`, h).Scan(&alreadyRan)
	if alreadyRan {
		fmt.Printf("hash %s exists skipping ddl \n", h)
		return
	}

	dropInvalidIndexIfNeeded(db, ddl)

	mustExec(db, ddl)
	mustExec(db, `insert into mediorum_migrations values ($1, now()) on conflict do nothing`, h)
}

// concurrentIndexNameRe captures the index name from a
// `CREATE INDEX CONCURRENTLY IF NOT EXISTS <name>` statement. A previous
// interrupted run can leave the index registered with indisvalid=false; the
// `IF NOT EXISTS` guard would then silently skip recreation forever, so we
// drop any invalid leftover before running the migration.
var concurrentIndexNameRe = regexp.MustCompile(`(?is)CREATE\s+INDEX\s+CONCURRENTLY\s+IF\s+NOT\s+EXISTS\s+([A-Za-z_][A-Za-z0-9_]*)`)

func dropInvalidIndexIfNeeded(db *sql.DB, ddl string) {
	m := concurrentIndexNameRe.FindStringSubmatch(ddl)
	if m == nil {
		return
	}
	// Unquoted Postgres identifiers are stored lower-case in pg_class.relname.
	name := strings.ToLower(m[1])

	var invalid bool
	err := db.QueryRow(`
		SELECT NOT i.indisvalid
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = $1
	`, name).Scan(&invalid)
	if err != nil || !invalid {
		return
	}

	// DROP INDEX CONCURRENTLY must run outside a transaction; mustExec uses
	// db.Exec which is fine because runMigration has no surrounding tx.
	mustExec(db, fmt.Sprintf(`DROP INDEX CONCURRENTLY IF EXISTS %s`, name))
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
