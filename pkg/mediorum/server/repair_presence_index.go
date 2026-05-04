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

// repairPresenceIndex holds the result of a bucket.List call as an in-memory
// map, allowing O(1) presence checks instead of per-key HeadObject calls.
type repairPresenceIndex struct {
	entries map[string]presenceEntry
}

func (idx *repairPresenceIndex) Lookup(key string) (presenceEntry, bool) {
	entry, ok := idx.entries[key]
	return entry, ok
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
		}
	}
}
