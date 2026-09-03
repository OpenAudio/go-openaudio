package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	"gocloud.dev/blob"
)

const (
	presenceBucketPrimary = "primary"
	presenceBucketArchive = "archive"

	// presenceLivenessSample is how many stored keys are checked against the
	// filesystem before the store is trusted for a cycle. See
	// verifyPresenceStoreLiveness.
	presenceLivenessSample = 64

	// presenceLivenessMaxMissRatio is the share of that sample that may be
	// absent before the store is treated as not describing this disk.
	// Individual blobs going missing is what repair exists to fix, so a few
	// misses are normal; most of them missing means something structural.
	presenceLivenessMaxMissRatio = 0.25
)

// BlobPresence records that this node holds Key in Bucket.
//
// Deliberately node-local and NOT registered with crudr: it describes one
// node's disk, not shared protocol state, and replicating it would tell peers
// something they must not act on.
//
// Only file:// buckets are tracked. Cloud backends paginate List server-side,
// so enumerating them is already linear and does not need a durable copy.
type BlobPresence struct {
	Bucket  string    `gorm:"primaryKey;not null"`
	Key     string    `gorm:"primaryKey;not null"`
	Size    int64     `gorm:"not null"`
	ModTime time.Time `gorm:"not null"`
}

func (BlobPresence) TableName() string { return "blob_presence" }

// BlobPresenceState records that a bucket has been fully enumerated at least
// once, which is what makes the per-key rows safe to read.
//
// Row count alone cannot answer that. The write path inserts rows as blobs
// land, so a node that has never walked still accumulates them — and a
// half-populated table read as authoritative would report most of the corpus
// missing and send repair off to re-pull all of it.
type BlobPresenceState struct {
	Bucket   string    `gorm:"primaryKey;not null"`
	WalkedAt time.Time `gorm:"not null"`
	Entries  int64     `gorm:"not null"`
}

func (BlobPresenceState) TableName() string { return "blob_presence_state" }

// bucketDSN returns the configured DSN for one of this node's buckets.
func (ss *MediorumServer) bucketDSN(b *blob.Bucket) string {
	if ss.archiveBucket != nil && b == ss.archiveBucket {
		return ss.Config.ArchiveBlobStoreDSN
	}
	return ss.Config.BlobStoreDSN
}

// bucketLabel names a bucket for the presence store and progress reporting.
func (ss *MediorumServer) bucketLabel(b *blob.Bucket) string {
	if ss.archiveBucket != nil && b == ss.archiveBucket {
		return presenceBucketArchive
	}
	return presenceBucketPrimary
}

// bucketForLabel is the inverse of bucketLabel. Returns nil for an archive row
// on a node with no archive bucket configured.
func (ss *MediorumServer) bucketForLabel(label string) *blob.Bucket {
	if label == presenceBucketArchive {
		return ss.archiveBucket
	}
	return ss.bucket
}

// presenceStoreEnabled reports whether the durable store backs this bucket.
// Only file:// buckets pay the walk the store exists to avoid.
func (ss *MediorumServer) presenceStoreEnabled(b *blob.Bucket) bool {
	if ss.pgPool == nil || !ss.Config.PresenceStoreEnabled {
		return false
	}
	_, isFile := fileBucketDir(ss.bucketDSN(b))
	return isFile
}

// repairBuckets returns the buckets a repair cycle will actually consult.
func (ss *MediorumServer) repairBuckets() []*blob.Bucket {
	buckets := []*blob.Bucket{ss.bucket}
	if ss.archiveBucket != nil && ss.Config.StoreAll {
		buckets = append(buckets, ss.archiveBucket)
	}
	return buckets
}

// recordBlobPresent notes a blob as held. Called after a successful write, on
// the same path that populates knownPresent.
//
// Ordering matters and is fixed by the caller: the blob is written and closed
// first, then recorded here. A failure between the two leaves a blob on disk
// with no row, which makes repair re-pull a blob it already has — wasteful but
// harmless and self-correcting. The reverse order would leave a row with no
// blob, and repair would skip a pull it needed.
func (ss *MediorumServer) recordBlobPresent(bucket *blob.Bucket, key string, size int64) {
	if !ss.presenceStoreEnabled(bucket) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := ss.pgPool.Exec(ctx, `
		insert into blob_presence (bucket, key, size, mod_time)
		values ($1, $2, $3, $4)
		on conflict (bucket, key) do update set size = excluded.size, mod_time = excluded.mod_time`,
		ss.bucketLabel(bucket), key, size, time.Now().UTC())
	if err != nil {
		// Never fail a write because bookkeeping failed; the next walk repairs it.
		ss.logger.Warn("failed to record blob presence", zap.String("key", key), zap.Error(err))
	}
}

// forgetBlobPresent drops a blob's row. Called wherever knownPresent is evicted
// because the blob is gone.
func (ss *MediorumServer) forgetBlobPresent(bucket *blob.Bucket, key string) {
	if !ss.presenceStoreEnabled(bucket) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := ss.pgPool.Exec(ctx,
		`delete from blob_presence where bucket = $1 and key = $2`,
		ss.bucketLabel(bucket), key)
	if err != nil {
		ss.logger.Warn("failed to forget blob presence", zap.String("key", key), zap.Error(err))
	}
}

// presenceForCIDs loads presence for exactly the keys one batch will touch.
//
// This is what replaces enumerating the whole bucket up front. Downstream is
// entirely unchanged — the batch loops keep their existing dispatch, and
// repairCidWithPolicy receives the same *repairPresenceIndex it always did.
// The index simply holds a batch's worth of entries rather than millions, and
// it reads current state rather than a snapshot frozen before the cycle began.
// That second part also fixes a real staleness bug: the whole-bucket index was
// never updated during a run, so every blob pulled mid-cycle stayed a "miss"
// in it for the rest of the cycle.
func (ss *MediorumServer) presenceForCIDs(ctx context.Context, cids []string) (*repairPresenceIndex, error) {
	// cidutil.ShardCID is Go logic with three branches, so keys are derived
	// here rather than in SQL. Dedupe within the batch: the same transcode CID
	// can appear under more than one upload.
	keys := make([]string, 0, len(cids))
	seen := make(map[string]struct{}, len(cids))
	for _, cid := range cids {
		if cid == "" {
			continue
		}
		key := cidutil.ShardCID(cid)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}

	index := newRepairPresenceIndex()
	if len(keys) == 0 {
		return index, nil
	}

	// Fetch both buckets' rows and let Lookup pick. A CID can legitimately live
	// in either bucket (a rank-flip orphan), and Lookup already reports
	// "missing" when the key is only in the bucket the caller did not ask for.
	// Filtering by bucket in SQL would lose that distinction, and the routing
	// decision itself stays in Go because it depends on rendezvous rank,
	// StoreAll and placement.
	rows, err := ss.pgPool.Query(ctx,
		`select bucket, key, size, mod_time from blob_presence where key = any($1)`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var label, key string
		var size int64
		var modTime time.Time
		if err := rows.Scan(&label, &key, &size, &modTime); err != nil {
			return nil, err
		}
		bucket := ss.bucketForLabel(label)
		if bucket == nil {
			continue
		}
		index.add(key, bucket, presenceEntry{Size: size, ModTime: modTime})
	}
	return index, rows.Err()
}

// savePresenceStore replaces this bucket's rows with what a walk just found and
// marks the bucket as fully enumerated. One transaction, so a crash midway
// leaves the previous contents rather than a partial index that would read as
// authoritative.
func (ss *MediorumServer) savePresenceStore(ctx context.Context, bucket *blob.Bucket, index *repairPresenceIndex) error {
	label := ss.bucketLabel(bucket)
	now := time.Now().UTC()

	// Snapshot into a compact slice rather than building [][]any up front: on a
	// multi-million-entry archive the boxed form is hundreds of MB, on nodes
	// where that is a meaningful share of available memory.
	type presenceRow struct {
		key  string
		size int64
	}
	index.mu.Lock()
	rows := make([]presenceRow, 0, len(index.entries))
	for k, v := range index.entries {
		if k.bucket != bucket {
			continue
		}
		rows = append(rows, presenceRow{key: k.key, size: v.Size})
	}
	index.mu.Unlock()

	tx, err := ss.pgPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	if _, err := tx.Exec(ctx, `delete from blob_presence where bucket = $1`, label); err != nil {
		return err
	}
	if _, err := tx.CopyFrom(ctx,
		pgx.Identifier{"blob_presence"},
		[]string{"bucket", "key", "size", "mod_time"},
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			return []any{label, rows[i].key, rows[i].size, now}, nil
		})); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		insert into blob_presence_state (bucket, walked_at, entries)
		values ($1, $2, $3)
		on conflict (bucket) do update set walked_at = excluded.walked_at, entries = excluded.entries`,
		label, now, int64(len(rows))); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// errPresenceStoreNotReady means the store cannot be used this cycle and the
// caller must enumerate the bucket instead.
var errPresenceStoreNotReady = errors.New("presence store not ready")

// presenceStoreReady reports whether every bucket this cycle will consult can
// be served from the store. It is all-or-nothing on purpose: a mixed
// file://-plus-cloud node falls back to enumerating everything, which is what
// it does today, rather than running two presence paths at once.
func (ss *MediorumServer) presenceStoreReady(ctx context.Context) error {
	for _, bucket := range ss.repairBuckets() {
		if !ss.presenceStoreEnabled(bucket) {
			return fmt.Errorf("%w: disabled, or %s is not a file:// bucket",
				errPresenceStoreNotReady, ss.bucketLabel(bucket))
		}
		var entries int64
		err := ss.pgPool.QueryRow(ctx,
			`select entries from blob_presence_state where bucket = $1`,
			ss.bucketLabel(bucket)).Scan(&entries)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s has never been walked",
				errPresenceStoreNotReady, ss.bucketLabel(bucket))
		}
		if err != nil {
			return err
		}
		dir, _ := fileBucketDir(ss.bucketDSN(bucket))
		if err := ss.verifyPresenceStoreLiveness(ctx, bucket, dir); err != nil {
			return err
		}
	}
	return nil
}

// verifyPresenceStoreLiveness checks that the store still describes this disk.
//
// This is the guard against the failure a durable index introduces. Reading
// presence from a table means never looking at the filesystem, so an archive
// that failed to mount, was replaced, or was emptied out of band would be
// reported as fully present and repair would skip every pull — silently
// serving nothing while looking healthy.
//
// It is a liveness check, not a completeness audit: it catches "this disk is
// not the one the store describes", which is the failure that loses data
// silently. Individual blobs that have gone missing are what repair is for, and
// the cleanup cycle's full walk is what reconciles the store properly.
func (ss *MediorumServer) verifyPresenceStoreLiveness(ctx context.Context, bucket *blob.Bucket, dir string) error {
	if fi, err := os.Stat(dir); err != nil {
		return fmt.Errorf("%w: blob dir %q is not readable: %v", errPresenceStoreNotReady, dir, err)
	} else if !fi.IsDir() {
		return fmt.Errorf("%w: blob dir %q is not a directory", errPresenceStoreNotReady, dir)
	}

	rows, err := ss.pgPool.Query(ctx,
		`select key from blob_presence where bucket = $1 limit $2`,
		ss.bucketLabel(bucket), presenceLivenessSample)
	if err != nil {
		return err
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}

	missing := 0
	for _, key := range keys {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(key))); err != nil {
			missing++
		}
	}
	if ratio := float64(missing) / float64(len(keys)); ratio > presenceLivenessMaxMissRatio {
		return fmt.Errorf("%w: %d of %d sampled keys missing under %q",
			errPresenceStoreNotReady, missing, len(keys), dir)
	}
	return nil
}
