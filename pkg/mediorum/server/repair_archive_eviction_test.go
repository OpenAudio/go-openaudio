package server

import (
	"context"
	"testing"
	"time"

	"github.com/erni27/imcache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gocloud.dev/blob"
	_ "gocloud.dev/blob/memblob"
)

// makeArchiveEvictionServer wires a StoreAll node with both buckets open, plus
// the knownPresent cache that the repair paths write through. Everything else
// comes from makeBucketSelectorServer.
func makeArchiveEvictionServer(t *testing.T, primary, archive *blob.Bucket, replicationFactor int) *MediorumServer {
	t.Helper()
	ss := makeBucketSelectorServer(t, primary, archive, true, replicationFactor, 0)
	ss.knownPresent = imcache.New[string, int64]()
	return ss
}

func openMemBuckets(t *testing.T) (*blob.Bucket, *blob.Bucket) {
	t.Helper()
	primary, err := blob.OpenBucket(context.Background(), "mem://")
	require.NoError(t, err)
	archive, err := blob.OpenBucket(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() {
		primary.Close()
		archive.Close()
	})
	return primary, archive
}

func newCleanupTracker(cleanup bool) *RepairTracker {
	return &RepairTracker{
		StartedAt:   time.Now(),
		CleanupMode: cleanup,
		Counters:    map[string]int{},
	}
}

func TestRelocateBetweenBucketsMovesBlobAndDropsSource(t *testing.T) {
	ctx := context.Background()
	primary, archive := openMemBuckets(t)
	ss := makeArchiveEvictionServer(t, primary, archive, 4)

	key := "relocate-me"
	data := []byte("some blob bytes")
	require.NoError(t, primary.WriteAll(ctx, key, data, nil))

	n, err := ss.relocateBetweenBuckets(ctx, key, archive, primary)
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), n)

	got, err := archive.ReadAll(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, data, got, "destination must hold the original bytes")

	exists, err := primary.Exists(ctx, key)
	require.NoError(t, err)
	assert.False(t, exists, "source copy must be reclaimed")

	// The destination copy is the one repair should now consider present.
	size, ok := ss.knownPresent.Get(ss.presenceCacheKey(key, archive))
	assert.True(t, ok)
	assert.Equal(t, int64(len(data)), size)
}

// The two buckets are usually different filesystems, so a relocate is a copy
// rather than a rename. If the destination write fails there is a window where
// deleting the source would destroy the only copy this node holds.
func TestRelocateBetweenBucketsKeepsSourceWhenDestinationFails(t *testing.T) {
	ctx := context.Background()
	primary, archive := openMemBuckets(t)
	ss := makeArchiveEvictionServer(t, primary, archive, 4)

	key := "relocate-fails"
	data := []byte("must survive")
	require.NoError(t, primary.WriteAll(ctx, key, data, nil))

	// Closing the destination makes NewWriter fail.
	require.NoError(t, archive.Close())

	_, err := ss.relocateBetweenBuckets(ctx, key, archive, primary)
	require.Error(t, err)

	got, readErr := primary.ReadAll(ctx, key)
	require.NoError(t, readErr, "source must survive a failed relocate")
	assert.Equal(t, data, got)
}

func TestRelocateBetweenBucketsErrorsWhenSourceMissing(t *testing.T) {
	ctx := context.Background()
	primary, archive := openMemBuckets(t)
	ss := makeArchiveEvictionServer(t, primary, archive, 4)

	_, err := ss.relocateBetweenBuckets(ctx, "nope", archive, primary)
	assert.Error(t, err)
}

// Repair must move an archive-routed blob out of primary locally rather than
// re-pulling it from a peer. Before the fix, presence resolved per bucket, so
// the blob read as missing in archive and repair pulled a second copy over the
// network while the primary copy stayed forever.
func TestRepairRelocatesLocallyInsteadOfPullingIntoArchive(t *testing.T) {
	const replicationFactor = 4
	ctx := context.Background()
	primary, archive := openMemBuckets(t)
	ss := makeArchiveEvictionServer(t, primary, archive, replicationFactor)

	// rank >= ReplicationFactor is what routes a CID to archive on a StoreAll node.
	cid := findCIDByRank(t, ss, replicationFactor)
	require.Equal(t, archive, ss.bucketForCID(cid, nil), "test CID must route to archive")

	data := []byte("already on this disk")
	require.NoError(t, primary.WriteAll(ctx, cid, data, nil))

	tracker := newCleanupTracker(false)
	policy := newRepairRetentionPolicy(ss.Config, time.Now())
	require.NoError(t, ss.repairCidWithPolicy(ctx, cid, nil, tracker, nil, policy, time.Time{}))

	assert.Equal(t, 1, tracker.Counters["relocated_between_buckets"])
	assert.Equal(t, 0, tracker.Counters["pull_mine_needed"], "must not reach for the network")
	assert.Equal(t, int64(len(data)), tracker.ContentSize)

	inArchive, err := archive.Exists(ctx, cid)
	require.NoError(t, err)
	assert.True(t, inArchive, "blob must land in the routed bucket")

	inPrimary, err := primary.Exists(ctx, cid)
	require.NoError(t, err)
	assert.False(t, inPrimary, "stale primary copy must be reclaimed")
}

// The duplicate that accumulated before this fix: archive already holds the
// blob and primary still has its pre-archive copy. Cleanup must drop primary
// and leave archive alone.
func TestRepairDropsRedundantPrimaryCopyWhenArchiveConfirmed(t *testing.T) {
	const replicationFactor = 4
	ctx := context.Background()
	primary, archive := openMemBuckets(t)
	ss := makeArchiveEvictionServer(t, primary, archive, replicationFactor)

	cid := findCIDByRank(t, ss, replicationFactor)
	require.Equal(t, archive, ss.bucketForCID(cid, nil))

	data := []byte("held twice")
	require.NoError(t, primary.WriteAll(ctx, cid, data, nil))
	require.NoError(t, archive.WriteAll(ctx, cid, data, nil))

	tracker := newCleanupTracker(true)
	policy := newRepairRetentionPolicy(ss.Config, time.Now())
	require.NoError(t, ss.repairCidWithPolicy(ctx, cid, nil, tracker, nil, policy, time.Time{}))

	assert.Equal(t, 1, tracker.Counters["archive_primary_duplicate_dropped"])
	assert.Equal(t, 0, tracker.Counters["archive_primary_duplicate_fail"])

	inPrimary, err := primary.Exists(ctx, cid)
	require.NoError(t, err)
	assert.False(t, inPrimary, "redundant primary copy must be dropped")

	got, err := archive.ReadAll(ctx, cid)
	require.NoError(t, err)
	assert.Equal(t, data, got, "archive copy must be untouched")

	// ContentSize counted the archive copy, never the duplicate.
	assert.Equal(t, int64(len(data)), tracker.ContentSize)
}

func TestRepairPrimaryCopyEvictionGuards(t *testing.T) {
	const replicationFactor = 4

	tests := []struct {
		name        string
		rank        int
		cleanup     bool
		storeAll    bool
		withArchive bool
		wantDropped bool
	}{
		{
			name:        "drops duplicate for archive-routed cid during cleanup",
			rank:        replicationFactor,
			cleanup:     true,
			storeAll:    true,
			withArchive: true,
			wantDropped: true,
		},
		{
			name:        "keeps primary outside cleanup mode",
			rank:        replicationFactor,
			cleanup:     false,
			storeAll:    true,
			withArchive: true,
		},
		{
			name:        "keeps primary when cid is not archive-routed",
			rank:        replicationFactor - 1,
			cleanup:     true,
			storeAll:    true,
			withArchive: true,
		},
		{
			name:        "keeps primary when no archive is configured",
			rank:        replicationFactor,
			cleanup:     true,
			storeAll:    true,
			withArchive: false,
		},
		{
			name:        "keeps primary when store all is off",
			rank:        replicationFactor,
			cleanup:     true,
			storeAll:    false,
			withArchive: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			primary, archive := openMemBuckets(t)
			if !tt.withArchive {
				archive = nil
			}
			ss := makeBucketSelectorServer(t, primary, archive, tt.storeAll, replicationFactor, 0)
			ss.knownPresent = imcache.New[string, int64]()

			cid := findCIDByRank(t, ss, tt.rank)
			data := []byte(tt.name)
			require.NoError(t, primary.WriteAll(ctx, cid, data, nil))
			if archive != nil {
				require.NoError(t, archive.WriteAll(ctx, cid, data, nil))
			}
			tracker := newCleanupTracker(tt.cleanup)
			policy := newRepairRetentionPolicy(ss.Config, time.Now())
			_ = ss.repairCidWithPolicy(ctx, cid, nil, tracker, nil, policy, time.Time{})

			inPrimary, err := primary.Exists(ctx, cid)
			require.NoError(t, err)
			if tt.wantDropped {
				assert.False(t, inPrimary, "expected the redundant primary copy to be dropped")
				assert.Equal(t, 1, tracker.Counters["archive_primary_duplicate_dropped"])
			} else {
				assert.Equal(t, 0, tracker.Counters["archive_primary_duplicate_dropped"],
					"eviction must not fire for this configuration")
			}
		})
	}
}
