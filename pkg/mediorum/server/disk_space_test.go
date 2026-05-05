package server

import (
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/registrar"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"gocloud.dev/blob"
	_ "gocloud.dev/blob/memblob"
)

// makeDiskSpaceServer is a minimal MediorumServer wired only for disk-space
// helpers. Cached free-bytes values are passed in directly so we don't depend
// on a real filesystem (the helpers fall back to the cached value when the DSN
// can't be statfs'd).
func makeDiskSpaceServer(env, primaryDSN, archiveDSN string, primaryFree, archiveFree uint64, archive *blob.Bucket, storeAll bool) *MediorumServer {
	hosts := []string{
		"http://node1.test",
		"http://node2.test",
		"http://node3.test",
		"http://node4.test",
		"http://node5.test",
	}
	return &MediorumServer{
		archiveBucket:    archive,
		mediorumPathFree: primaryFree,
		archivePathFree:  archiveFree,
		rendezvousHasher: common.NewRendezvousHasher(hosts, nil),
		logger:           zap.NewNop(),
		Config: MediorumConfig{
			Env:                  env,
			Self:                 registrar.Peer{Host: hosts[0]},
			ReplicationFactor:    3,
			BlobStoreDSN:         primaryDSN,
			ArchiveBlobStoreDSN:  archiveDSN,
			StoreAll:             storeAll,
		},
	}
}

const (
	gb         = uint64(1e9)
	plentyFree = 100 * gb
	tightFree  = 5 * gb // below the 10GB threshold
)

func TestDiskHasSpace_NonProdAlwaysAllows(t *testing.T) {
	// dev/stage/test should never gate writes on disk pressure
	for _, env := range []string{"dev", "stage", "test", ""} {
		ss := makeDiskSpaceServer(env, "file:///tmp/nope", "", tightFree, 0, nil, false)
		assert.True(t, ss.diskHasSpace(), "env=%s should always allow", env)
	}
}

func TestDiskHasSpace_NonFileBackendAlwaysAllows(t *testing.T) {
	// S3 / GCS / Azure don't have local disk constraints; the helper must
	// short-circuit to true regardless of the cached free-bytes value.
	ss := makeDiskSpaceServer("prod", "s3://my-bucket?region=us-east-1", "", tightFree, 0, nil, false)
	assert.True(t, ss.diskHasSpace())
}

func TestDiskHasSpace_FilePrimaryUsesFallbackFreeBytes(t *testing.T) {
	// The DSN points at a path we can't statfs, so the helper falls back to
	// the cached mediorumPathFree value (this mirrors the real prod path
	// when the mount is briefly unavailable).
	t.Run("plenty of space", func(t *testing.T) {
		ss := makeDiskSpaceServer("prod", "file:///nonexistent/path", "", plentyFree, 0, nil, false)
		assert.True(t, ss.diskHasSpace())
	})
	t.Run("below threshold", func(t *testing.T) {
		ss := makeDiskSpaceServer("prod", "file:///nonexistent/path", "", tightFree, 0, nil, false)
		assert.False(t, ss.diskHasSpace())
	})
}

func TestDiskHasSpace_ArchiveBucketChecked(t *testing.T) {
	// Archive disk only matters when archive can actually receive writes:
	// archiveBucket open AND StoreAll true. Use a real (mem) archive bucket
	// so archiveBucket is non-nil; gating still operates off DSN free-bytes.
	archive := openMemBucket(t)

	t.Run("primary plenty, archive tight -> false", func(t *testing.T) {
		ss := makeDiskSpaceServer("prod", "file:///nonexistent/primary", "file:///nonexistent/archive",
			plentyFree, tightFree, archive, true)
		assert.False(t, ss.diskHasSpace())
	})
	t.Run("primary tight, archive plenty -> false", func(t *testing.T) {
		ss := makeDiskSpaceServer("prod", "file:///nonexistent/primary", "file:///nonexistent/archive",
			tightFree, plentyFree, archive, true)
		assert.False(t, ss.diskHasSpace())
	})
	t.Run("both plenty -> true", func(t *testing.T) {
		ss := makeDiskSpaceServer("prod", "file:///nonexistent/primary", "file:///nonexistent/archive",
			plentyFree, plentyFree, archive, true)
		assert.True(t, ss.diskHasSpace())
	})

	t.Run("archive open but StoreAll=false -> archive ignored", func(t *testing.T) {
		// non-StoreAll node with stray archive DSN: archive is logged as
		// "unused" at startup and no CID will ever route there. Don't gate
		// writes on its disk pressure.
		ss := makeDiskSpaceServer("prod", "file:///nonexistent/primary", "file:///nonexistent/archive",
			plentyFree, tightFree, archive, false)
		assert.True(t, ss.diskHasSpace())
	})

	t.Run("archive DSN set but bucket nil -> archive ignored", func(t *testing.T) {
		// Defensive case: DSN string set but bucket open failed / not opened.
		ss := makeDiskSpaceServer("prod", "file:///nonexistent/primary", "file:///nonexistent/archive",
			plentyFree, tightFree, nil, true)
		assert.True(t, ss.diskHasSpace())
	})
}

func TestDiskHasSpaceForCID_RoutesToCorrectBucket(t *testing.T) {
	// Use a real (mem) archive bucket so bucketForCID can route — disk-space
	// gating logic operates off the DSN string, not the bucket itself.
	archive := openMemBucket(t)
	primary := openMemBucket(t)
	hosts := []string{
		"http://node1.test", "http://node2.test", "http://node3.test",
		"http://node4.test", "http://node5.test",
	}
	hasher := common.NewRendezvousHasher(hosts, nil)

	build := func(primaryFree, archiveFree uint64) *MediorumServer {
		return &MediorumServer{
			bucket:           primary,
			archiveBucket:    archive,
			mediorumPathFree: primaryFree,
			archivePathFree:  archiveFree,
			rendezvousHasher: hasher,
			logger:           zap.NewNop(),
			Config: MediorumConfig{
				Env:                 "prod",
				Self:                registrar.Peer{Host: hosts[0]},
				ReplicationFactor:   3,
				StoreAll:            true,
				BlobStoreDSN:        "file:///nonexistent/primary",
				ArchiveBlobStoreDSN: "file:///nonexistent/archive",
			},
		}
	}

	primaryCID := findCIDByRank(t, build(plentyFree, plentyFree), 0)  // routes to primary
	archiveCID := findCIDByRank(t, build(plentyFree, plentyFree), 4) // routes to archive

	t.Run("archive full does not block primary write", func(t *testing.T) {
		ss := build(plentyFree, tightFree)
		assert.True(t, ss.diskHasSpaceForCID(primaryCID, nil))
		assert.False(t, ss.diskHasSpaceForCID(archiveCID, nil))
	})

	t.Run("primary full does not block archive write", func(t *testing.T) {
		ss := build(tightFree, plentyFree)
		assert.False(t, ss.diskHasSpaceForCID(primaryCID, nil))
		assert.True(t, ss.diskHasSpaceForCID(archiveCID, nil))
	})

	t.Run("placementHosts force primary path even for high-rank CID", func(t *testing.T) {
		// archive is full, primary has space; if the upload comes with an
		// explicit placement list, the disk check must use primary's
		// free-bytes (because writes will go to primary)
		ss := build(plentyFree, tightFree)
		placement := []string{ss.Config.Self.Host, hosts[1]}
		assert.True(t, ss.diskHasSpaceForCID(archiveCID, placement))
	})

	t.Run("non-prod always true", func(t *testing.T) {
		ss := build(tightFree, tightFree)
		ss.Config.Env = "dev"
		assert.True(t, ss.diskHasSpaceForCID(primaryCID, nil))
		assert.True(t, ss.diskHasSpaceForCID(archiveCID, nil))
	})
}
