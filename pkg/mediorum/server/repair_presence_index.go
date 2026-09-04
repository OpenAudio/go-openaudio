package server

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	"gocloud.dev/blob"
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
type repairPresenceIndex struct {
	entries map[indexKey]presenceEntry
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
	index := &repairPresenceIndex{
		entries: make(map[indexKey]presenceEntry),
	}

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

	sawEscaped, err := walkFileBucketIntoIndex(ctx, dir, bucket, index)
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
	for k := range index.entries {
		if k.bucket == bucket {
			delete(index.entries, k)
		}
	}
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

// walkFileBucketIntoIndex adds every blob under dir to index in a single pass.
// It reports whether it encountered a hex-escaped path, in which case the
// caller must fall back to bucket.List — decoding requires gocloud's internal
// escape package, and a wrong key would make repair re-pull a blob it has.
func walkFileBucketIntoIndex(ctx context.Context, dir string, bucket *blob.Bucket, index *repairPresenceIndex) (bool, error) {
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
		index.entries[indexKey{key: key, bucket: bucket}] = presenceEntry{
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}
		return nil
	})
	if err != nil {
		return sawEscaped, err
	}
	return sawEscaped, nil
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
		index.entries[indexKey{key: obj.Key, bucket: bucket}] = presenceEntry{
			Size:    obj.Size,
			ModTime: obj.ModTime,
		}
	}
}
