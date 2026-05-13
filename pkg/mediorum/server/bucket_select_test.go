package server

import (
	"context"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/registrar"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"gocloud.dev/blob"
	_ "gocloud.dev/blob/memblob"
)

// makeBucketSelectorServer returns a *MediorumServer wired with just enough
// fields for bucketForCID / isArchiveCID / disk-space helpers to exercise their
// logic. No DB, no HTTP, no peers — just config + buckets + hasher.
//
// Hosts are five fake URLs; selfIdx picks which one is "this node." The
// ranking depends on the CID, so tests pick CIDs whose rank is known via
// rendezvousAllHosts.
func makeBucketSelectorServer(t *testing.T, primary, archive *blob.Bucket, storeAll bool, replicationFactor, selfIdx int) *MediorumServer {
	t.Helper()
	hosts := []string{
		"http://node1.test",
		"http://node2.test",
		"http://node3.test",
		"http://node4.test",
		"http://node5.test",
	}
	hasher := common.NewRendezvousHasher(hosts, nil)
	return &MediorumServer{
		bucket:           primary,
		archiveBucket:    archive,
		rendezvousHasher: hasher,
		logger:           zap.NewNop(),
		Config: MediorumConfig{
			Self:              registrar.Peer{Host: hosts[selfIdx]},
			ReplicationFactor: replicationFactor,
			StoreAll:          storeAll,
		},
	}
}

// findCIDByRank returns a CID string whose rendezvous rank for hosts[selfIdx]
// matches wantRank. We brute-force a handful of candidates because the test
// only needs *some* CID with a known rank, not a specific one.
func findCIDByRank(t *testing.T, ss *MediorumServer, wantRank int) string {
	t.Helper()
	for i := 0; i < 5000; i++ {
		cid := "QmTestCID" + string(rune('A'+(i%26))) + string(rune('a'+((i/26)%26))) + string(rune('0'+(i/(26*26)%10)))
		ordered := ss.rendezvousHasher.Rank(cid)
		for rank, h := range ordered {
			if h == ss.Config.Self.Host && rank == wantRank {
				return cid
			}
		}
	}
	t.Fatalf("could not synthesize CID with rank %d", wantRank)
	return ""
}

func openMemBucket(t *testing.T) *blob.Bucket {
	t.Helper()
	b, err := blob.OpenBucket(context.Background(), "mem://")
	if err != nil {
		t.Fatalf("openMemBucket: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

// TestBucketForCID covers the full truth table for the selector.
func TestBucketForCID(t *testing.T) {
	primary := openMemBucket(t)
	archive := openMemBucket(t)

	t.Run("archive nil always returns primary", func(t *testing.T) {
		ss := makeBucketSelectorServer(t, primary, nil, true, 3, 0)
		cid := findCIDByRank(t, ss, 4) // would be archive if archive were set
		assert.Same(t, primary, ss.bucketForCID(cid, nil))
		assert.False(t, ss.isArchiveCID(cid, nil))
	})

	t.Run("StoreAll false returns primary even with archive set", func(t *testing.T) {
		ss := makeBucketSelectorServer(t, primary, archive, false, 3, 0)
		cid := findCIDByRank(t, ss, 4)
		assert.Same(t, primary, ss.bucketForCID(cid, nil))
		assert.False(t, ss.isArchiveCID(cid, nil))
	})

	t.Run("rank in replication range -> primary", func(t *testing.T) {
		ss := makeBucketSelectorServer(t, primary, archive, true, 3, 0)
		cid := findCIDByRank(t, ss, 0) // top of the rank
		assert.Same(t, primary, ss.bucketForCID(cid, nil))
		assert.False(t, ss.isArchiveCID(cid, nil))
	})

	t.Run("rank exactly at boundary (== ReplicationFactor) -> archive", func(t *testing.T) {
		ss := makeBucketSelectorServer(t, primary, archive, true, 3, 0)
		cid := findCIDByRank(t, ss, 3) // == ReplicationFactor, so out of range
		assert.Same(t, archive, ss.bucketForCID(cid, nil))
		assert.True(t, ss.isArchiveCID(cid, nil))
	})

	t.Run("rank well past replication range -> archive", func(t *testing.T) {
		ss := makeBucketSelectorServer(t, primary, archive, true, 3, 0)
		cid := findCIDByRank(t, ss, 4)
		assert.Same(t, archive, ss.bucketForCID(cid, nil))
		assert.True(t, ss.isArchiveCID(cid, nil))
	})

	t.Run("placementHosts always uses primary", func(t *testing.T) {
		ss := makeBucketSelectorServer(t, primary, archive, true, 3, 0)
		cid := findCIDByRank(t, ss, 4)
		// placement-driven uploads must never go to archive even when StoreAll
		// would otherwise route there
		placement := []string{ss.Config.Self.Host, "http://other.test"}
		assert.Same(t, primary, ss.bucketForCID(cid, placement))
		assert.False(t, ss.isArchiveCID(cid, placement))
	})

	t.Run("ReplicationFactor=1, rank 1 -> archive", func(t *testing.T) {
		ss := makeBucketSelectorServer(t, primary, archive, true, 1, 0)
		cid := findCIDByRank(t, ss, 1)
		assert.Same(t, archive, ss.bucketForCID(cid, nil))
	})

	t.Run("ReplicationFactor=1, rank 0 -> primary", func(t *testing.T) {
		ss := makeBucketSelectorServer(t, primary, archive, true, 1, 0)
		cid := findCIDByRank(t, ss, 0)
		assert.Same(t, primary, ss.bucketForCID(cid, nil))
	})

	t.Run("self not in hash ring (myRank<0) -> primary", func(t *testing.T) {
		// Build a server whose Self.Host isn't in the rendezvous host list,
		// simulating misconfiguration / mid-registration.
		ss := makeBucketSelectorServer(t, primary, archive, true, 3, 0)
		ss.Config.Self = registrar.Peer{Host: "http://stranger.test"}
		// any CID will rank stranger.test at -1
		assert.Same(t, primary, ss.bucketForCID("QmTestCIDA", nil))
		assert.False(t, ss.isArchiveCID("QmTestCIDA", nil))
	})
}
