package server

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/slices"
)

func TestRepairRetentionPolicyShouldStoreRecent(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		config    MediorumConfig
		createdAt time.Time
		want      bool
	}{
		{
			name:      "default settings do not retain recent",
			config:    MediorumConfig{},
			createdAt: now,
			want:      false,
		},
		{
			name:      "store recent retains upload inside ttl",
			config:    MediorumConfig{StoreRecent: true, StoreRecentTTL: 30 * 24 * time.Hour},
			createdAt: now.Add(-29 * 24 * time.Hour),
			want:      true,
		},
		{
			name:      "store recent does not retain upload outside ttl",
			config:    MediorumConfig{StoreRecent: true, StoreRecentTTL: 30 * 24 * time.Hour},
			createdAt: now.Add(-31 * 24 * time.Hour),
			want:      false,
		},
		{
			name:      "store all ignores store recent",
			config:    MediorumConfig{StoreAll: true, StoreRecent: true, StoreRecentTTL: 30 * 24 * time.Hour},
			createdAt: now,
			want:      false,
		},
		{
			name:      "zero timestamp is not retained",
			config:    MediorumConfig{StoreRecent: true, StoreRecentTTL: 30 * 24 * time.Hour},
			createdAt: time.Time{},
			want:      false,
		},
		{
			name:      "zero ttl uses one year default",
			config:    MediorumConfig{StoreRecent: true},
			createdAt: now.Add(-364 * 24 * time.Hour),
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := newRepairRetentionPolicy(tt.config, now)
			assert.Equal(t, tt.want, policy.shouldStoreRecent(tt.createdAt))
		})
	}
}

func TestRepairCidStoreRecentPullsRecentUpload(t *testing.T) {
	ctx := context.Background()
	source, target, cid, data := testRepairSourceAndNonPreferredTarget(t, "store-recent-pulls-recent-upload")

	require.NoError(t, source.replicateToMyBucket(ctx, cid, bytes.NewReader(data), nil))
	require.NoError(t, target.dropFromMyBucket(cid))

	disabledTracker := &RepairTracker{
		StartedAt:   time.Now(),
		CleanupMode: false,
		Counters:    map[string]int{},
	}
	disabledPolicy := newRepairRetentionPolicy(MediorumConfig{}, time.Now())
	require.NoError(t, target.repairCidWithPolicy(ctx, cid, nil, disabledTracker, nil, disabledPolicy, time.Now()))
	assert.False(t, target.haveInMyBucket(cid))

	recentTracker := &RepairTracker{
		StartedAt:   time.Now(),
		CleanupMode: false,
		Counters:    map[string]int{},
	}
	recentPolicy := newRepairRetentionPolicy(MediorumConfig{
		StoreRecent:    true,
		StoreRecentTTL: 365 * 24 * time.Hour,
	}, time.Now())
	require.NoError(t, target.repairCidWithPolicy(ctx, cid, nil, recentTracker, nil, recentPolicy, time.Now()))
	assert.True(t, target.haveInMyBucket(cid))
	assert.Equal(t, 1, recentTracker.Counters["pull_mine_success"])
}

func TestRepairCidStoreRecentKeepsOverReplicatedRecentUpload(t *testing.T) {
	ctx := context.Background()
	_, target, cid, data := testRepairSourceAndNonPreferredTarget(t, "store-recent-keeps-overreplicated-upload")

	require.NoError(t, target.replicateToMyBucket(ctx, cid, bytes.NewReader(data), nil))
	markLocalBlobOld(t, target, cid, 8*24*time.Hour)
	target.mediorumPathFree = 0
	target.mediorumPathSize = 1

	recentPolicy := newRepairRetentionPolicy(MediorumConfig{
		StoreRecent:    true,
		StoreRecentTTL: 365 * 24 * time.Hour,
	}, time.Now())
	recentTracker := &RepairTracker{
		StartedAt:   time.Now(),
		CleanupMode: true,
		Counters:    map[string]int{},
	}
	require.NoError(t, target.repairCidWithPolicy(ctx, cid, nil, recentTracker, nil, recentPolicy, time.Now()))
	assert.True(t, target.haveInMyBucket(cid))
	assert.Zero(t, recentTracker.Counters["delete_over_replicated_success"])

	oldPolicy := newRepairRetentionPolicy(MediorumConfig{
		StoreRecent:    true,
		StoreRecentTTL: 365 * 24 * time.Hour,
	}, time.Now())
	oldTracker := &RepairTracker{
		StartedAt:   time.Now(),
		CleanupMode: true,
		Counters:    map[string]int{},
	}
	target.mediorumPathFree = 0
	target.mediorumPathSize = 1
	require.NoError(t, target.repairCidWithPolicy(ctx, cid, nil, oldTracker, nil, oldPolicy, time.Now().Add(-366*24*time.Hour)))
	assert.False(t, target.haveInMyBucket(cid))
	assert.Equal(t, 1, oldTracker.Counters["delete_over_replicated_success"])
}

func testRepairSourceAndNonPreferredTarget(t *testing.T, seed string) (*MediorumServer, *MediorumServer, string, []byte) {
	t.Helper()

	data := []byte(seed)
	cid, err := cidutil.ComputeFileCID(bytes.NewReader(data))
	require.NoError(t, err)

	byHost := map[string]*MediorumServer{}
	for _, s := range testNetwork {
		byHost[s.Config.Self.Host] = s
	}

	preferred, _ := testNetwork[0].rendezvousAllHosts(cid)
	require.Greater(t, len(preferred), testNetwork[0].Config.ReplicationFactor+2)

	source := byHost[preferred[0]]
	target := byHost[preferred[len(preferred)-1]]
	require.NotNil(t, source)
	require.NotNil(t, target)
	require.Greater(t, slices.Index(preferred, target.Config.Self.Host), target.Config.ReplicationFactor+2)

	return source, target, cid, data
}

func markLocalBlobOld(t *testing.T, ss *MediorumServer, cid string, age time.Duration) {
	t.Helper()

	u, err := url.Parse(ss.Config.BlobStoreDSN)
	require.NoError(t, err)

	key := cidutil.ShardCID(cid)
	path := filepath.Join(u.Path, filepath.FromSlash(key))
	old := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(path, old, old))
}
