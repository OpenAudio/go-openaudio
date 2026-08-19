package server

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testNetworkRunRepair(cleanup bool) {
	wg := sync.WaitGroup{}
	wg.Add(len(testNetwork))
	for _, s := range testNetwork {
		s := s
		go func() {
			err := s.runRepair(context.Background(), &RepairTracker{StartedAt: time.Now(), CleanupMode: cleanup, Counters: map[string]int{}})
			if err != nil {
				panic(err)
			}
			wg.Done()
		}()
	}
	wg.Wait()
}

func testNetworkLocateBlob(cid string) []string {
	ctx := context.Background()
	key := cidutil.ShardCID(cid)
	result := []string{}
	for _, s := range testNetwork {
		if ok, _ := s.bucket.Exists(ctx, key); ok {
			result = append(result, s.Config.Self.Host)
		}
	}
	return result
}

func TestRepair(t *testing.T) {
	ctx := context.Background()
	replicationFactor := testNetwork[0].Config.ReplicationFactor

	// First, write a blob only to its highest-ranked storage node.
	data := []byte("repair test")
	cid, err := cidutil.ComputeFileCID(bytes.NewReader(data))
	assert.NoError(t, err)
	byHost := map[string]*MediorumServer{}
	for _, server := range testNetwork {
		byHost[server.Config.Self.Host] = server
	}
	preferred, _ := testNetwork[0].rendezvousAllHosts(cid)
	ss := byHost[preferred[0]]
	require.NotNil(t, ss)

	err = ss.replicateToMyBucket(ctx, cid, bytes.NewReader(data), nil)
	assert.NoError(t, err)

	// create a dummy upload for it?
	ss.crud.Create(Upload{
		ID:          "testing",
		OrigFileCID: cid,
		CreatedAt:   time.Now(),
	})

	// verify we can get it "manually"
	{
		s2 := testNetwork[1]
		u, err := s2.peerGetUpload(ss.Config.Self.Host, "testing")
		assert.NoError(t, err)
		assert.Equal(t, cid, u.OrigFileCID)

		var uploads []Upload
		resp, err := s2.reqClient.R().SetSuccessResult(&uploads).Get(ss.Config.Self.Host + "/uploads")
		assert.NoError(t, err)
		assert.Equal(t, 200, resp.StatusCode)
		assert.Len(t, uploads, 1)
		assert.NotEmpty(t, resp.GetHeader("x-took"))
	}

	applyCoreOpsFrom(t, ss)

	// assert it only exists on 1 host
	{
		hosts := testNetworkLocateBlob(cid)
		assert.Len(t, hosts, 1)
	}

	// tell all servers do repair
	testNetworkRunRepair(true)

	// Repair fills exactly the configured rendezvous replica set.
	{
		hosts := testNetworkLocateBlob(cid)
		assert.Len(t, hosts, replicationFactor)
	}

	// --------------------------
	//
	// now over-replicate file
	//
	for _, server := range testNetwork {
		ss.replicateFileToHost(ctx, server.Config.Self.Host, cid, bytes.NewReader(data), nil)
	}

	// assert over-replicated
	{
		hosts := testNetworkLocateBlob(cid)
		assert.Len(t, hosts, len(testNetwork))
	}

	// tell all servers do cleanup
	testNetworkRunRepair(true)

	// Fresh extra copies stay for the one-week drain period.
	{
		hosts := testNetworkLocateBlob(cid)
		assert.Len(t, hosts, len(testNetwork))
	}

	for _, server := range testNetwork {
		markLocalBlobOld(t, server, cid, 8*24*time.Hour)
	}
	testNetworkRunRepair(true)

	// Once the drain period expires, cleanup keeps exactly R copies.
	hosts := testNetworkLocateBlob(cid)
	assert.Len(t, hosts, replicationFactor)
}

func TestRepairPrunesAtReplicationBoundaryAfterDrain(t *testing.T) {
	const replicationFactor = 4

	base := testNetwork[0]
	ss := &MediorumServer{
		bucket:           base.bucket,
		archiveBucket:    base.archiveBucket,
		rendezvousHasher: base.rendezvousHasher,
		logger:           base.logger,
		knownPresent:     base.knownPresent,
		Config:           base.Config,
	}
	ss.Config.ReplicationFactor = replicationFactor
	// Free space no longer changes how many replicas cleanup retains.
	ss.mediorumPathFree = 90
	ss.mediorumPathSize = 100

	tests := []struct {
		name     string
		rank     int
		age      time.Duration
		storeAll bool
		wantBlob bool
	}{
		{
			name:     "keeps last assigned replica",
			rank:     replicationFactor - 1,
			age:      8 * 24 * time.Hour,
			wantBlob: true,
		},
		{
			name:     "keeps first extra replica during drain week",
			rank:     replicationFactor,
			age:      6 * 24 * time.Hour,
			wantBlob: true,
		},
		{
			name: "deletes first extra replica after drain week",
			rank: replicationFactor,
			age:  8 * 24 * time.Hour,
		},
		{
			name:     "store all keeps extra replica after drain week",
			rank:     replicationFactor,
			age:      8 * 24 * time.Hour,
			storeAll: true,
			wantBlob: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ss.Config.StoreAll = tt.storeAll
			cid := findCIDByRank(t, ss, tt.rank)
			require.NoError(t, ss.dropFromMyBucket(cid))
			t.Cleanup(func() { require.NoError(t, ss.dropFromMyBucket(cid)) })
			require.NoError(t, ss.replicateToMyBucket(context.Background(), cid, bytes.NewReader([]byte(tt.name)), nil))
			markLocalBlobOld(t, ss, cid, tt.age)

			tracker := &RepairTracker{
				StartedAt:   time.Now(),
				CleanupMode: true,
				Counters:    map[string]int{},
			}
			policy := newRepairRetentionPolicy(ss.Config, time.Now())
			require.NoError(t, ss.repairCidWithPolicy(context.Background(), cid, nil, tracker, nil, policy, time.Time{}))
			assert.Equal(t, tt.wantBlob, ss.haveInMyBucket(cid))
		})
	}
}

func TestBuildRepairPresenceIndexIncludesLocalBlob(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]

	data := []byte("presence-index-local-blob")
	cid, err := cidutil.ComputeFileCID(bytes.NewReader(data))
	assert.NoError(t, err)
	assert.NoError(t, ss.replicateToMyBucket(ctx, cid, bytes.NewReader(data), nil))

	index, err := ss.buildRepairPresenceIndex(ctx)
	assert.NoError(t, err)

	entry, ok := index.Lookup(cidutil.ShardCID(cid), ss.bucket)
	assert.True(t, ok)
	assert.Equal(t, int64(len(data)), entry.Size)
}

func TestRepairCidWithPresenceIndexUsesListedState(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]

	data := []byte("presence-index-repair-path")
	cid, err := cidutil.ComputeFileCID(bytes.NewReader(data))
	assert.NoError(t, err)
	assert.NoError(t, ss.replicateToMyBucket(ctx, cid, bytes.NewReader(data), nil))

	index, err := ss.buildRepairPresenceIndex(ctx)
	assert.NoError(t, err)

	key := cidutil.ShardCID(cid)
	ss.knownPresent.Remove(ss.presenceCacheKey(key, ss.bucket))
	assert.NoError(t, ss.dropFromMyBucket(cid))

	tracker := &RepairTracker{
		StartedAt:   time.Now(),
		CleanupMode: false,
		Counters:    map[string]int{},
	}

	assert.NoError(t, ss.repairCid(ctx, cid, []string{ss.Config.Self.Host}, tracker, index))
	assert.Equal(t, 1, tracker.Counters["already_have"])
	assert.Equal(t, 1, tracker.Counters["qm_cids_list_index_hit"])
	assert.Equal(t, 0, tracker.Counters["qm_cids_list_index_miss"])
}

func TestRepairCidUsesKnownPresentOutsideCleanup(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]

	data := []byte("known-present-fast-path")
	cid, err := cidutil.ComputeFileCID(bytes.NewReader(data))
	assert.NoError(t, err)
	assert.NoError(t, ss.replicateToMyBucket(ctx, cid, bytes.NewReader(data), nil))

	tracker := &RepairTracker{
		StartedAt:   time.Now(),
		CleanupMode: false,
		Counters:    map[string]int{},
	}

	assert.NoError(t, ss.repairCid(ctx, cid, []string{ss.Config.Self.Host}, tracker, nil))
	assert.Equal(t, 1, tracker.Counters["already_have"])
	assert.Equal(t, 1, tracker.Counters["repair_known_present"])
}

// The rendezvous ranking is every node on the network, so an unobtainable CID
// would otherwise be tried against all of them — each miss costing a dial or a
// hung peer's timeout. The bound keeps that to the replica set plus a margin
// for ring churn.
func TestMaxPullAttempts(t *testing.T) {
	cases := []struct {
		replicationFactor int
		want              int
	}{
		{4, 4 + pullAttemptMargin},
		{1, 1 + pullAttemptMargin},
		{0, pullAttemptMargin},
		{-5, 1}, // misconfigured: never zero, or no host is ever tried
	}
	for _, c := range cases {
		ss := &MediorumServer{Config: MediorumConfig{ReplicationFactor: c.replicationFactor}}
		if got := ss.maxPullAttempts(); got != c.want {
			t.Fatalf("ReplicationFactor=%d: maxPullAttempts()=%d want %d", c.replicationFactor, got, c.want)
		}
	}
}
