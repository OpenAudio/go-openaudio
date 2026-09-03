package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enablePresenceStore turns the store on for one test and restores the default
// afterwards. The default is off, so every test that exercises the store has to
// opt in explicitly — which is itself part of what is being asserted.
func enablePresenceStore(t *testing.T, ss *MediorumServer) {
	t.Helper()
	prev := ss.Config.PresenceStoreEnabled
	ss.Config.PresenceStoreEnabled = true
	t.Cleanup(func() { ss.Config.PresenceStoreEnabled = prev })

	ctx := context.Background()
	_, err := ss.pgPool.Exec(ctx, `delete from blob_presence`)
	require.NoError(t, err)
	_, err = ss.pgPool.Exec(ctx, `delete from blob_presence_state`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ss.pgPool.Exec(ctx, `delete from blob_presence`)
		_, _ = ss.pgPool.Exec(ctx, `delete from blob_presence_state`)
	})
}

// The store is opt-in. With OPENAUDIO_PRESENCE_STORE_ENABLED unset, a repair
// cycle must behave exactly as it did before the store existed: nothing written
// on the replication path, and presence resolved by enumerating buckets.
func TestPresenceStoreDisabledByDefault(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	require.False(t, ss.Config.PresenceStoreEnabled, "the default must be off")

	// The write path is a no-op...
	ss.recordBlobPresent(ss.bucket, "some/key", 1)
	var rows int64
	require.NoError(t, ss.pgPool.QueryRow(ctx,
		`select count(*) from blob_presence`).Scan(&rows))
	assert.Zero(t, rows, "nothing may be written while the store is disabled")

	// ...and the cycle falls back to enumeration.
	err := ss.presenceStoreReady(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, errPresenceStoreNotReady)
}

// The batch query is what replaces enumerating the whole bucket, so it has to
// answer what the whole-bucket index answered for these keys: present with the
// right size, or absent.
func TestPresenceForCIDsResolvesBatchKeys(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	enablePresenceStore(t, ss)

	held := "QmY7Yh4UquoXHLPFo2XbhXkhBvFoPwmQUSa92pxnxjQuPU"
	absent := "QmZZzzXyvAKjGp1uN7oeBSCv1G958kZ6naoMSZPt68vtjf"
	ss.recordBlobPresent(ss.bucket, cidutil.ShardCID(held), 1234)

	// The same CID twice, plus an empty one: dedupe must collapse them.
	index, err := ss.presenceForCIDs(ctx, []string{held, held, absent, ""})
	require.NoError(t, err)

	entry, ok := index.Lookup(cidutil.ShardCID(held), ss.bucket)
	require.True(t, ok, "a recorded blob must resolve as present")
	assert.Equal(t, int64(1234), entry.Size)

	_, ok = index.Lookup(cidutil.ShardCID(absent), ss.bucket)
	assert.False(t, ok, "an unrecorded blob must resolve as missing")

	assert.Len(t, index.entries, 1, "duplicate and empty CIDs must not add entries")
}

// A cycle that enumerated its buckets reuses that index for every batch and
// never queries — the default path, and the one that must cost nothing.
func TestPresenceForBatchReusesCycleIndex(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]

	cycleIndex := newRepairPresenceIndex()
	tracker := &RepairTracker{Counters: map[string]int{}, mu: newTrackerMutex()}

	gathered := false
	got, err := ss.presenceForBatch(ctx, cycleIndex, tracker, func() []string {
		gathered = true
		return nil
	})
	require.NoError(t, err)
	assert.Same(t, cycleIndex, got, "the cycle index must be handed straight through")
	assert.False(t, gathered, "the batch must not even be walked to gather keys")
	assert.Zero(t, tracker.Counters["presence_batch_queries"])
}

// The write path records presence and the delete path forgets it, so the store
// tracks the bucket between enumerations rather than only at walk time.
func TestRecordAndForgetBlobPresent(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	enablePresenceStore(t, ss)

	const key = "zzz/some-recorded-key"
	ss.recordBlobPresent(ss.bucket, key, 4242)

	index, err := ss.presenceForCIDs(ctx, []string{key})
	require.NoError(t, err)
	entry, ok := index.Lookup(key, ss.bucket)
	require.True(t, ok, "write path must record presence")
	assert.Equal(t, int64(4242), entry.Size)

	ss.forgetBlobPresent(ss.bucket, key)

	index, err = ss.presenceForCIDs(ctx, []string{key})
	require.NoError(t, err)
	_, ok = index.Lookup(key, ss.bucket)
	assert.False(t, ok, "delete path must forget presence")
}

// Row count cannot say whether a bucket has been enumerated: the write path
// inserts rows as blobs land, so a node that has never walked still accumulates
// them. Reading a half-populated table as authoritative would report most of
// the corpus missing and send repair off to re-pull all of it.
func TestPresenceStoreNotReadyUntilFullyWalked(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	enablePresenceStore(t, ss)

	ss.recordBlobPresent(ss.bucket, "some/key", 1)
	err := ss.presenceStoreReady(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, errPresenceStoreNotReady)
	assert.Contains(t, err.Error(), "never been walked")

	_, err = ss.buildRepairPresenceIndex(ctx)
	require.NoError(t, err)
	assert.NoError(t, ss.presenceStoreReady(ctx),
		"a completed enumeration should make the store readable")
}

// The guard against the failure a durable index introduces: reading presence
// from a table means never looking at the filesystem, so an archive that failed
// to mount, was replaced, or was emptied out of band would be reported as fully
// present and repair would skip every pull.
func TestVerifyPresenceStoreLivenessRejectsWrongDisk(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	enablePresenceStore(t, ss)

	keys := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("sh%02d/blob", i)
		keys = append(keys, key)
		ss.recordBlobPresent(ss.bucket, key, 1)
	}

	// An empty directory stands in for a mount that came up wrong.
	assert.ErrorIs(t, ss.verifyPresenceStoreLiveness(ctx, ss.bucket, t.TempDir()),
		errPresenceStoreNotReady)

	// A directory that actually holds the blobs passes.
	populated := t.TempDir()
	for _, key := range keys {
		path := filepath.Join(populated, filepath.FromSlash(key))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	}
	assert.NoError(t, ss.verifyPresenceStoreLiveness(ctx, ss.bucket, populated))
}

// A missing root is the unmounted-archive case and must never read as "fine".
func TestVerifyPresenceStoreLivenessRejectsMissingDir(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	enablePresenceStore(t, ss)

	ss.recordBlobPresent(ss.bucket, "some/key", 1)
	assert.ErrorIs(t,
		ss.verifyPresenceStoreLiveness(ctx, ss.bucket, filepath.Join(t.TempDir(), "not-mounted")),
		errPresenceStoreNotReady)
}
