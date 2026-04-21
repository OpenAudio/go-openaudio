package server

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"

	"github.com/stretchr/testify/assert"
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
	replicationFactor := 5

	ss := testNetwork[0]

	// first, write a blob only to my storage
	data := []byte("repair test")
	cid, err := cidutil.ComputeFileCID(bytes.NewReader(data))
	assert.NoError(t, err)
	err = ss.replicateToMyBucket(ctx, cid, bytes.NewReader(data))
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

	// force sweep (since blob changes SkipBroadcast)
	for _, s := range testNetwork {
		s.crud.ForceSweep()
	}

	// assert it only exists on 1 host
	{
		hosts := testNetworkLocateBlob(cid)
		assert.Len(t, hosts, 1)
	}

	// tell all servers do repair
	testNetworkRunRepair(true)

	// assert it exists on R hosts
	{
		hosts := testNetworkLocateBlob(cid)

		// cleanup will permit blob on R+2
		// so assert upper threshold thusly
		assert.LessOrEqual(t, len(hosts), replicationFactor+2)
	}

	// --------------------------
	//
	// now over-replicate file
	//
	for _, server := range testNetwork {
		ss.replicateFileToHost(ctx, server.Config.Self.Host, cid, bytes.NewReader(data))
	}

	// assert over-replicated
	{
		hosts := testNetworkLocateBlob(cid)
		assert.Len(t, hosts, len(testNetwork))
	}

	// tell all servers do cleanup
	testNetworkRunRepair(true)

	// assert R copies
	if false {
		hosts := testNetworkLocateBlob(cid)
		assert.Len(t, hosts, replicationFactor)
	}

	// ----------------------
	// now make one of the servers "lose" a file
	if false {
		byHost := map[string]*MediorumServer{}
		for _, s := range testNetwork {
			byHost[s.Config.Self.Host] = s
		}

		rendezvousOrder := []*MediorumServer{}
		preferred, _ := ss.rendezvousAllHosts(cid)
		for _, h := range preferred {
			rendezvousOrder = append(rendezvousOrder, byHost[h])
		}

		// make leader lose file
		leader := rendezvousOrder[0]
		leader.dropFromMyBucket(cid)

		// normally a standby server wouldn't pull this file
		standby := rendezvousOrder[replicationFactor+2]
		err = standby.runRepair(ctx, &RepairTracker{StartedAt: time.Now(), CleanupMode: false, Counters: map[string]int{}})
		assert.NoError(t, err)
		assert.False(t, standby.hostHasBlob(standby.Config.Self.Host, cid))

		// running repair in cleanup mode... standby will observe that #1 doesn't have blob so will pull it
		err = standby.runRepair(ctx, &RepairTracker{StartedAt: time.Now(), CleanupMode: true, Counters: map[string]int{}})
		assert.NoError(t, err)
		assert.True(t, standby.hostHasBlob(standby.Config.Self.Host, cid))

		// leader re-gets lost file when repair runs
		err = leader.runRepair(ctx, &RepairTracker{StartedAt: time.Now(), CleanupMode: false, Counters: map[string]int{}})
		assert.NoError(t, err)
		assert.True(t, leader.hostHasBlob(leader.Config.Self.Host, cid))

		// standby drops file after leader has it back
		err = standby.runRepair(ctx, &RepairTracker{StartedAt: time.Now(), CleanupMode: true, Counters: map[string]int{}})
		assert.NoError(t, err)
		assert.False(t, standby.hostHasBlob(standby.Config.Self.Host, cid))
	}

}

func TestBuildRepairPresenceIndexIncludesLocalBlob(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]

	data := []byte("presence-index-local-blob")
	cid, err := cidutil.ComputeFileCID(bytes.NewReader(data))
	assert.NoError(t, err)
	assert.NoError(t, ss.replicateToMyBucket(ctx, cid, bytes.NewReader(data)))

	index, err := ss.buildRepairPresenceIndex(ctx)
	assert.NoError(t, err)

	entry, ok := index.Lookup(cidutil.ShardCID(cid))
	assert.True(t, ok)
	assert.Equal(t, int64(len(data)), entry.Size)
}

func TestRepairCidWithPresenceIndexUsesListedState(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]

	data := []byte("presence-index-repair-path")
	cid, err := cidutil.ComputeFileCID(bytes.NewReader(data))
	assert.NoError(t, err)
	assert.NoError(t, ss.replicateToMyBucket(ctx, cid, bytes.NewReader(data)))

	index, err := ss.buildRepairPresenceIndex(ctx)
	assert.NoError(t, err)

	key := cidutil.ShardCID(cid)
	ss.knownPresent.Remove(key)
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
	assert.NoError(t, ss.replicateToMyBucket(ctx, cid, bytes.NewReader(data)))

	tracker := &RepairTracker{
		StartedAt:   time.Now(),
		CleanupMode: false,
		Counters:    map[string]int{},
	}

	assert.NoError(t, ss.repairCid(ctx, cid, []string{ss.Config.Self.Host}, tracker, nil))
	assert.Equal(t, 1, tracker.Counters["already_have"])
	assert.Equal(t, 1, tracker.Counters["repair_known_present"])
}
