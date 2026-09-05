package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"gocloud.dev/blob"
	"golang.org/x/sync/errgroup"
)

// fileblobAttrsExt is the sidecar file gocloud's fileblob driver writes next to
// each blob to hold content-type/MD5. It is not itself a blob, and fileblob's
// own List skips it, so the direct walk must skip it too.
const fileblobAttrsExt = ".attrs"

// fileblobEscapeMarker is the prefix gocloud's escape.HexEscape emits when it
// has to escape a rune in a key (e.g. "__0x2f_"). Every key mediorum writes is
// a cidutil.ShardCID output — alphanumerics, '/' and '.' only — none of which
// fileblob escapes on a '/'-separated filesystem, so the direct walk can map
// path to key verbatim. Decoding an escaped path would need gocloud's internal
// escape package, so if we ever see one we fall back to bucket.List instead of
// guessing.
const fileblobEscapeMarker = "__0x"

const (
	presenceWalkBatch = 4096
)

// errEscapedPath signals that a hex-escaped key was found and the caller must
// fall back to bucket.List. It travels as an error so that finding one cancels
// the other walkers rather than letting them finish work that gets discarded.
var errEscapedPath = errors.New("hex-escaped blob path")

type presenceEntry struct {
	Size    int64
	ModTime time.Time
}

// indexKey scopes a presence entry to a specific bucket. A CID can legitimately
// exist in both buckets (e.g. a rank-flip orphan plus the freshly-pulled correct
// copy); a single map keyed only by storage key would have the second listing
// overwrite the first, hiding the bucket the caller is asking about.
type indexKey struct {
	key    string
	bucket *blob.Bucket
}

// repairPresenceIndex holds the result of a bucket listing as an in-memory
// map, allowing O(1) presence checks instead of per-key HeadObject calls.
//
// Entries are added concurrently by the file walk, so all writes go through
// add() and the mutex it takes. Reads (Lookup) happen only after the build has
// finished and are not synchronized.
type repairPresenceIndex struct {
	mu      sync.Mutex
	entries map[indexKey]presenceEntry
}

func newRepairPresenceIndex() *repairPresenceIndex {
	return &repairPresenceIndex{entries: make(map[indexKey]presenceEntry)}
}

func (idx *repairPresenceIndex) add(key string, bucket *blob.Bucket, entry presenceEntry) {
	idx.mu.Lock()
	idx.entries[indexKey{key: key, bucket: bucket}] = entry
	idx.mu.Unlock()
}

// dropBucket removes every entry belonging to bucket, so a bucket whose walk
// has to be redone through another code path starts from nothing.
func (idx *repairPresenceIndex) dropBucket(bucket *blob.Bucket) {
	idx.mu.Lock()
	for k := range idx.entries {
		if k.bucket == bucket {
			delete(idx.entries, k)
		}
	}
	idx.mu.Unlock()
}

// Lookup returns the entry for key in wantBucket. A key that exists only in
// the *other* bucket (rank-flip orphan) reports missing here so repair will
// pull a fresh copy into the bucket bucketForCID selected.
func (idx *repairPresenceIndex) Lookup(key string, wantBucket *blob.Bucket) (presenceEntry, bool) {
	entry, ok := idx.entries[indexKey{key: key, bucket: wantBucket}]
	return entry, ok
}

// blobPresentIn reports whether key exists in b, preferring the presence index
// over a per-key existence check. The index already listed both buckets on a
// StoreAll node with an archive (see buildRepairPresenceIndex), so the common
// case costs a map lookup.
//
// A nil bucket reports false: callers use this to ask about "the other bucket",
// which does not exist when no archive is configured.
func (ss *MediorumServer) blobPresentIn(ctx context.Context, idx *repairPresenceIndex, b *blob.Bucket, key string) bool {
	if b == nil {
		return false
	}
	if idx != nil {
		_, ok := idx.Lookup(key, b)
		return ok
	}
	ok, err := b.Exists(ctx, key)
	return err == nil && ok
}

func (ss *MediorumServer) buildRepairPresenceIndex(ctx context.Context) (*repairPresenceIndex, error) {
	index := newRepairPresenceIndex()

	if err := ss.listBucketIntoIndex(ctx, ss.bucket, ss.Config.BlobStoreDSN, index); err != nil {
		return nil, err
	}
	// Only list archive when it can actually receive routing. With StoreAll
	// off, bucketForCID never returns archive — listing it is pure overhead
	// (and potentially expensive for cloud backends with many objects).
	if ss.archiveBucket != nil && ss.Config.StoreAll {
		if err := ss.listBucketIntoIndex(ctx, ss.archiveBucket, ss.Config.ArchiveBlobStoreDSN, index); err != nil {
			return nil, err
		}
	}
	return index, nil
}

// listBucketIntoIndex adds every blob in bucket to index.
//
// For file:// buckets it walks the directory tree directly rather than going
// through bucket.List. gocloud's fileblob driver has no filesystem cursor: its
// ListPaged re-runs filepath.WalkDir from the root on *every* page and discards
// the keys it already returned, and it opens + JSON-decodes a ".attrs" sidecar
// for each file on each of those walks. Listing N objects at fileblob's default
// page size of 1000 therefore costs N²/1000 directory entries. A 1M-object
// archive is ~1e9 entry visits via List versus ~1e6 via a single walk — the
// difference between days and minutes, and it grows worse as the node fills.
//
// Non-file backends (s3, gs, azblob) paginate server-side with a continuation
// token, so List is already linear for them and is used unchanged.
func (ss *MediorumServer) listBucketIntoIndex(ctx context.Context, bucket *blob.Bucket, dsn string, index *repairPresenceIndex) error {
	dir, isFile := fileBucketDir(dsn)
	if !isFile {
		return listIntoIndex(ctx, bucket, index)
	}

	sawEscaped, err := ss.walkFileBucket(ctx, dir, bucket, index)
	if err != nil {
		return err
	}
	if !sawEscaped {
		return nil
	}

	// A key on disk is hex-escaped, so the verbatim path->key mapping is not
	// safe for this bucket. Drop what we collected and let gocloud decode.
	ss.logger.Warn("hex-escaped blob path found; falling back to bucket.List for this bucket",
		zap.String("dir", dir))
	index.dropBucket(bucket)
	return listIntoIndex(ctx, bucket, index)
}

// fileBucketDir returns the filesystem directory backing a file:// blob DSN,
// applying the same mapping as fileblob's URLOpener.OpenBucketURL. It reports
// false for any other scheme.
func fileBucketDir(dsn string) (string, bool) {
	if !strings.HasPrefix(dsn, "file://") {
		return "", false
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", false
	}
	path := u.Path
	// Host "." means a relative path, so drop the leading "/" — matches
	// fileblob.URLOpener.OpenBucketURL.
	if u.Host == "." || os.PathSeparator != '/' {
		path = strings.TrimPrefix(path, "/")
	}
	if path == "" {
		return "", false
	}
	return filepath.FromSlash(path), true
}

// walkFileBucket adds every blob under dir to index, reporting whether it
// encountered a hex-escaped path — in which case the caller must fall back to
// bucket.List, since decoding requires gocloud's internal escape package and a
// wrong key would make repair re-pull a blob it has.
//
// At PresenceWalkConcurrency 1, the default, this is the original serial walk
// unchanged: one filepath.WalkDir over the whole tree, lexical order, no
// batched root reads. A node that sets nothing gets exactly the behaviour it
// had before this option existed.
//
// Above 1 it opts into walkFileBucketConcurrent, which is worth doing only on a
// bucket large enough for the walk to dominate a repair cycle.
func (ss *MediorumServer) walkFileBucket(ctx context.Context, dir string, bucket *blob.Bucket, index *repairPresenceIndex) (bool, error) {
	if ss.Config.PresenceWalkConcurrency <= 1 {
		return walkFileBucketSerial(ctx, dir, bucket, index)
	}
	return walkFileBucketConcurrent(ctx, dir, bucket, index,
		ss.Config.PresenceWalkConcurrency, presenceWalkBatch)
}

// walkFileBucketSerial is the walk as it was before concurrency was an option.
//
// Kept verbatim rather than expressed as walkFileBucketConcurrent with a limit
// of one: the concurrent path also reads the bucket root in batches, visits
// shards in directory rather than lexical order, and pays an extra lstat per
// shard because filepath.WalkDir stats its own root. None of those differences
// matter for correctness, but "the default changes nothing" is only true if the
// default runs this code.
func walkFileBucketSerial(ctx context.Context, dir string, bucket *blob.Bucket, index *repairPresenceIndex) (bool, error) {
	// Verify the root before walking. filepath.WalkDir reports a missing or
	// unmounted root through the callback's err argument, and we deliberately
	// skip per-entry errors below — without this check an unmounted archive
	// would yield a silently empty index and repair would re-pull everything.
	if fi, err := os.Stat(dir); err != nil {
		return false, fmt.Errorf("blob dir %q is not readable: %w", dir, err)
	} else if !fi.IsDir() {
		return false, fmt.Errorf("blob dir %q is not a directory", dir)
	}

	sawEscaped := false
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Couldn't read this entry; fileblob's own List skips these rather
			// than failing the whole listing, so match that behavior.
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, fileblobAttrsExt) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		key := filepath.ToSlash(rel)
		if strings.Contains(key, fileblobEscapeMarker) {
			sawEscaped = true
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			// Raced with a delete between readdir and stat; treat as absent.
			return nil
		}
		index.add(key, bucket, presenceEntry{Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return sawEscaped, err
	}
	return sawEscaped, nil
}

func walkFileBucketConcurrent(ctx context.Context, dir string, bucket *blob.Bucket, index *repairPresenceIndex, concurrency, batch int) (bool, error) {
	// Verify the root before walking. filepath.WalkDir reports a missing or
	// unmounted root through the callback's err argument, and we deliberately
	// skip per-entry errors below — without this check an unmounted archive
	// would yield a silently empty index and repair would re-pull everything.
	if fi, err := os.Stat(dir); err != nil {
		return false, fmt.Errorf("blob dir %q is not readable: %w", dir, err)
	} else if !fi.IsDir() {
		return false, fmt.Errorf("blob dir %q is not a directory", dir)
	}

	root, err := os.Open(dir)
	if err != nil {
		return false, fmt.Errorf("blob dir %q is not readable: %w", dir, err)
	}
	defer root.Close()

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)

	// Read the root in batches rather than all at once. On a store-all node the
	// root holds one entry per shard prefix — millions of them — and a single
	// ReadDir would hold the whole list in memory before any blob was visited.
	for {
		if gctx.Err() != nil {
			break
		}
		ents, readErr := root.ReadDir(batch)
		for _, ent := range ents {
			if !ent.IsDir() {
				// A key with no shard prefix (cidutil.ShardCID passes some CIDs
				// through unchanged) lands directly in the root.
				if err := indexFileEntry(dir, filepath.Join(dir, ent.Name()), ent, bucket, index); err != nil {
					// Let the workers already running settle before reporting.
					_ = g.Wait()
					return walkOutcome(err)
				}
				continue
			}
			shard := filepath.Join(dir, ent.Name())
			g.Go(func() error { return walkShardIntoIndex(gctx, dir, shard, bucket, index) })
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			// Give the workers already running a chance to finish or fail so
			// their error is not masked by this one.
			_ = g.Wait()
			return false, fmt.Errorf("reading blob dir %q: %w", dir, readErr)
		}
	}

	if err := g.Wait(); err != nil {
		return walkOutcome(err)
	}
	// A cancelled walk must not look like an empty bucket. If the batch loop
	// exited on cancellation before any worker ran, no worker error carries the
	// signal, and returning (false, nil) here would hand repair a complete-
	// looking index with nothing in it — the same failure the root check above
	// exists to prevent.
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return false, nil
}

// walkShardIntoIndex indexes every blob under one shard directory.
func walkShardIntoIndex(ctx context.Context, root, shard string, bucket *blob.Bucket, index *repairPresenceIndex) error {
	return filepath.WalkDir(shard, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Couldn't read this entry; fileblob's own List skips these rather
			// than failing the whole listing, so match that behavior.
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		return indexFileEntry(root, path, d, bucket, index)
	})
}

// indexFileEntry adds one file to the index, or reports errEscapedPath if its
// key cannot be derived from its path.
func indexFileEntry(root, path string, d fs.DirEntry, bucket *blob.Bucket, index *repairPresenceIndex) error {
	if strings.HasSuffix(path, fileblobAttrsExt) {
		return nil
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil
	}
	key := filepath.ToSlash(rel)
	if strings.Contains(key, fileblobEscapeMarker) {
		return errEscapedPath
	}
	info, err := d.Info()
	if err != nil {
		// Raced with a delete between readdir and stat; treat as absent.
		return nil
	}
	index.add(key, bucket, presenceEntry{Size: info.Size(), ModTime: info.ModTime()})
	return nil
}

// walkOutcome converts the errgroup result into the (sawEscaped, err) pair the
// caller expects. An escaped path is a signal, not a failure.
func walkOutcome(err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	if errors.Is(err, errEscapedPath) {
		return true, nil
	}
	return false, err
}

// listIntoIndex populates index via the gocloud List API. Used for non-file
// backends, which paginate server-side, and as the correctness fallback for
// file buckets holding hex-escaped keys.
func listIntoIndex(ctx context.Context, bucket *blob.Bucket, index *repairPresenceIndex) error {
	iter := bucket.List(nil)
	for {
		obj, err := iter.Next(ctx)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if obj == nil || obj.IsDir {
			continue
		}
		index.add(obj.Key, bucket, presenceEntry{Size: obj.Size, ModTime: obj.ModTime})
	}
}
