package server

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestLogThrottleAllowsFirstThenSuppresses(t *testing.T) {
	var th logThrottle

	assert.True(t, th.allow("key", time.Minute))
	for i := 0; i < 100; i++ {
		assert.False(t, th.allow("key", time.Minute))
	}
}

func TestLogThrottleKeysAreIndependent(t *testing.T) {
	var th logThrottle

	assert.True(t, th.allow("primary", time.Minute))
	assert.True(t, th.allow("archive", time.Minute))
	assert.False(t, th.allow("primary", time.Minute))
	assert.False(t, th.allow("archive", time.Minute))
}

func TestLogThrottleAllowsAgainAfterInterval(t *testing.T) {
	var th logThrottle

	assert.True(t, th.allow("key", 20*time.Millisecond))
	assert.False(t, th.allow("key", 20*time.Millisecond))
	time.Sleep(30 * time.Millisecond)
	assert.True(t, th.allow("key", 20*time.Millisecond))
}

func TestLogThrottleConcurrent(t *testing.T) {
	// hammer one key from many goroutines: exactly one may win, and the
	// throttle must be race-free (validated under -race)
	var th logThrottle
	var allowed atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if th.allow("key", time.Minute) {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(1), allowed.Load())
}

func TestDsnHasSpaceWarnsOncePerInterval(t *testing.T) {
	// file DSN that can't be statfs'd with a tight fallback: every call takes
	// the warn path and returns false, but only the first call may log
	ss := makeDiskSpaceServer("prod", "file:///nonexistent/path", "", tightFree, 0, nil, false)
	core, logs := observer.New(zapcore.WarnLevel)
	ss.logger = zap.New(core)

	for i := 0; i < 100; i++ {
		assert.False(t, ss.diskHasSpace(), "throttling must never change the result")
	}

	assert.Equal(t, 1, logs.Len(), "repeated disk warns must be throttled")
}

func TestDsnHasSpaceThrottleDoesNotAffectResults(t *testing.T) {
	// same server, alternating outcomes across DSNs: results must stay
	// correct while logging is suppressed
	archive := openMemBucket(t)
	ss := makeDiskSpaceServer("prod", "file:///nonexistent/primary", "file:///nonexistent/archive",
		plentyFree, tightFree, archive, true)
	core, logs := observer.New(zapcore.WarnLevel)
	ss.logger = zap.New(core)

	for i := 0; i < 50; i++ {
		assert.True(t, ss.dsnHasSpace(ss.Config.BlobStoreDSN, ss.mediorumPathFree))
		assert.False(t, ss.dsnHasSpace(ss.Config.ArchiveBlobStoreDSN, ss.archivePathFree))
		assert.False(t, ss.diskHasSpace())
	}

	// both DSNs take the statfs-failed warn path; each may log once per
	// interval regardless of how many call paths check it
	assert.Equal(t, 2, logs.Len())
}
