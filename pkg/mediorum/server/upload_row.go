package server

import (
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"slices"
	"sync"
)

// errUploadRowUnchanged is what an updateUploadRow mutation returns to say the
// row needs no write. The helper hands back the row as it read it and emits no
// op, which keeps a no-op rewrite from being relayed to every node.
var errUploadRowUnchanged = errors.New("upload row unchanged")

// uploadRowLockStripes bounds the lock set. An upload maps to a stripe by a
// hash of its ID, so the array never grows with the table and two unrelated
// uploads sharing a stripe merely queue behind each other for the length of
// one row rewrite.
const uploadRowLockStripes = 1024

// uploadRowLocks serializes this node's read-modify-write updates to one
// uploads row. See updateUploadRow for why the row needs one.
type uploadRowLocks struct {
	stripes [uploadRowLockStripes]sync.Mutex
}

func (l *uploadRowLocks) lock(id string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(id))
	return &l.stripes[h.Sum32()%uploadRowLockStripes]
}

// updateUploadRow rewrites one uploads row as a single read-modify-write under
// this node's lock for that row: it reads the row fresh, applies mutate to it,
// and publishes the result through crudr. It returns the row as written, or
// as read when mutate reports it unchanged or fails.
//
// Every uploads write is a whole-row snapshot: crudr.Update upserts every
// column and emits the row as an op. So two local writers that each read the
// row, change their own fields, and write it back lose to each other whenever
// their windows overlap, because whichever writes second carries the other's
// fields as they stood before the first wrote. The upload handlers hand one
// new row to the transcode worker and the replication worker in the same
// instant, one to claim it and the other to record where the original landed,
// and when peers already hold the blob the mirror write lands inside the few
// milliseconds of the claim. The claim's transcoded_by then vanishes from the
// row, the completion that re-reads it publishes done with no transcoder, and
// everything keyed on that column -- transcode stats, store-all intake, the
// boot-time reset of stuck jobs -- misreads the upload from then on.
//
// The lock is per node, not per network. Peers apply this node's ops in the
// order it wrote them and never write rows they did not create, so serializing
// the writers here is what makes those ops a consistent history. mutate runs
// under the lock: it must not do I/O or update another uploads row, since a
// nested update on the same stripe would deadlock.
func (ss *MediorumServer) updateUploadRow(id string, mutate func(u *Upload) error) (*Upload, error) {
	mu := ss.uploadRowLocks.lock(id)
	mu.Lock()
	defer mu.Unlock()

	var upload Upload
	if err := ss.crud.DB.Where("id = ?", id).First(&upload).Error; err != nil {
		return nil, fmt.Errorf("failed to get upload from DB: %w", err)
	}
	if err := mutate(&upload); err != nil {
		if errors.Is(err, errUploadRowUnchanged) {
			return &upload, nil
		}
		return &upload, err
	}
	if err := ss.crud.Update(&upload); err != nil {
		return nil, err
	}
	return &upload, nil
}

// cloneUpload copies an upload for a second worker.
//
// The upload handlers hand one new row to both the replication worker and the
// transcode worker. The transcode worker writes TranscodeResults and
// TranscodedMirrors on its copy while the replication worker may still be
// reading them, which is a data race on the map itself, whatever values either
// side observes. The maps and slices are the only state the two share.
func cloneUpload(u *Upload) *Upload {
	c := *u
	c.TranscodeResults = maps.Clone(u.TranscodeResults)
	if c.TranscodeResults == nil {
		c.TranscodeResults = map[string]string{}
	}
	c.Mirrors = slices.Clone(u.Mirrors)
	c.TranscodedMirrors = slices.Clone(u.TranscodedMirrors)
	c.PlacementHosts = slices.Clone(u.PlacementHosts)
	return &c
}
