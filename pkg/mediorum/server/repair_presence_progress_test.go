package server

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/persistence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The counter is what both the periodic log line and the health endpoint read,
// so it has to agree with what the walk actually indexed. A count that drifts
// from the index would make a healthy walk look stalled, which is the exact
// confusion this progress reporting exists to remove.
func TestPresenceWalkCounterTracksWalk(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	bucket, err := persistence.Open("file://" + dir + "?no_tmp_dir=true")
	require.NoError(t, err)
	defer bucket.Close()

	const shards, perShard = 6, 4
	for s := 0; s < shards; s++ {
		for b := 0; b < perShard; b++ {
			require.NoError(t, bucket.WriteAll(ctx,
				fmt.Sprintf("sh%02d/blob-%d", s, b), bytes.Repeat([]byte("x"), b+1), nil))
		}
	}
	// A root-level key has no shard directory of its own.
	require.NoError(t, bucket.WriteAll(ctx, "unsharded", []byte("x"), nil))

	index := &repairPresenceIndex{entries: map[indexKey]presenceEntry{}}
	prog := newPresenceWalkCounter("archive", dir)
	sawEscaped, err := walkFileBucketConcurrent(ctx, dir, bucket, index, prog, 4, 2)
	require.NoError(t, err)
	require.False(t, sawEscaped)

	snap := prog.snapshot()
	assert.Equal(t, int64(shards*perShard+1), snap.Files, "counter must match blobs indexed")
	assert.Equal(t, int64(len(index.entries)), snap.Files, "counter must match the index")
	assert.Equal(t, int64(shards), snap.Shards, "one shard per top-level directory")
	assert.Equal(t, "archive", snap.Bucket)
	assert.Equal(t, dir, snap.Dir)
	assert.NotEmpty(t, snap.ElapsedHuman)
}

// The walk passes a nil counter in tests and anywhere progress is not tracked,
// so the increments must tolerate it rather than panic mid-walk.
func TestPresenceWalkCounterNilSafe(t *testing.T) {
	var p *presenceWalkCounter
	assert.NotPanics(t, func() {
		p.addFile()
		p.addShard()
	})
}

// health reports nil when no walk is running, so operators can tell "not
// walking" from "walking, 0 files so far".
//
// Deliberately calls presenceWalkProgress rather than getHealth: getHealth
// reads the stats fields that monitorMetrics writes without taking
// statsMutex, so exercising it directly trips the race detector on a
// pre-existing data race unrelated to this change.
func TestPresenceWalkProgressNilWhenIdle(t *testing.T) {
	assert.Nil(t, testNetwork[0].presenceWalkProgress())
}
