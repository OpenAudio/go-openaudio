package server

import (
	"context"
	"io"
	"time"

	"gocloud.dev/blob"
)

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

// repairPresenceIndex holds the result of a bucket.List call as an in-memory
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

func (ss *MediorumServer) buildRepairPresenceIndex(ctx context.Context) (*repairPresenceIndex, error) {
	index := &repairPresenceIndex{
		entries: make(map[indexKey]presenceEntry),
	}

	if err := listIntoIndex(ctx, ss.bucket, index); err != nil {
		return nil, err
	}
	// Only list archive when it can actually receive routing. With StoreAll
	// off, bucketForCID never returns archive — listing it is pure overhead
	// (and potentially expensive for cloud backends with many objects).
	if ss.archiveBucket != nil && ss.Config.StoreAll {
		if err := listIntoIndex(ctx, ss.archiveBucket, index); err != nil {
			return nil, err
		}
	}
	return index, nil
}

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
