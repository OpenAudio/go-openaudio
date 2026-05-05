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
	// Bucket records which bucket the listing came from. Repair must check
	// this against bucketForCID(cid, placementHosts) for each lookup; a key
	// present in the *wrong* bucket (e.g. an orphan from a rank flip) must
	// be treated as missing so the repair pull lands in the correct bucket.
	Bucket *blob.Bucket
}

// repairPresenceIndex holds the result of a bucket.List call as an in-memory
// map, allowing O(1) presence checks instead of per-key HeadObject calls.
type repairPresenceIndex struct {
	entries map[string]presenceEntry
}

// Lookup returns the entry for key only if the listing came from wantBucket.
// A merged index that contains the key under a different bucket reports
// "missing" — repair will then pull the blob into the bucket it expects.
func (idx *repairPresenceIndex) Lookup(key string, wantBucket *blob.Bucket) (presenceEntry, bool) {
	entry, ok := idx.entries[key]
	if !ok {
		return entry, false
	}
	if entry.Bucket != wantBucket {
		return presenceEntry{}, false
	}
	return entry, true
}

func (ss *MediorumServer) buildRepairPresenceIndex(ctx context.Context) (*repairPresenceIndex, error) {
	index := &repairPresenceIndex{
		entries: make(map[string]presenceEntry),
	}

	if err := listIntoIndex(ctx, ss.bucket, index); err != nil {
		return nil, err
	}
	if ss.archiveBucket != nil {
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
		index.entries[obj.Key] = presenceEntry{
			Size:    obj.Size,
			ModTime: obj.ModTime,
			Bucket:  bucket,
		}
	}
}
