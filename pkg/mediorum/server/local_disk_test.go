package server

import (
	"math"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tus/tusd/v2/pkg/handler"
)

func localDiskTestServer(t *testing.T) *MediorumServer {
	t.Helper()
	ss := testNetwork[0]
	original := ss.Config.Env
	ss.Config.Env = "prod"
	t.Cleanup(func() { ss.Config.Env = original })
	return ss
}

func TestLocalDirHasSpaceFor(t *testing.T) {
	t.Run("non-prod is never gated", func(t *testing.T) {
		ss := testNetwork[0]
		require.True(t, ss.localDirHasSpaceFor("/definitely/not/a/real/path", math.MaxUint64/2))
	})

	t.Run("an unmeasurable directory is allowed, not refused", func(t *testing.T) {
		ss := localDiskTestServer(t)
		require.True(t, ss.localDirHasSpaceFor("/definitely/not/a/real/path", 0),
			"an unreadable mount is not evidence of a full one")
	})

	t.Run("a real directory with room is allowed", func(t *testing.T) {
		ss := localDiskTestServer(t)
		require.True(t, ss.localDirHasSpaceFor(t.TempDir(), 1<<20))
	})

	t.Run("a request larger than the disk is refused", func(t *testing.T) {
		ss := localDiskTestServer(t)
		require.False(t, ss.localDirHasSpaceFor(t.TempDir(), math.MaxUint64/2),
			"no filesystem can take an exabyte")
	})

	// free <= needBytes+reserve rather than free-needBytes <= reserve, because
	// the latter underflows on unsigned when the request exceeds the disk --
	// which would read as astronomically free and admit it.
	t.Run("an oversized request does not underflow into acceptance", func(t *testing.T) {
		ss := localDiskTestServer(t)
		require.False(t, ss.localDirHasSpaceFor(t.TempDir(), math.MaxUint64-1))
	})

	t.Run("the reserve alone refuses a full disk", func(t *testing.T) {
		ss := localDiskTestServer(t)

		original := localDiskReserveBytes
		localDiskReserveBytes = math.MaxUint64 - 1
		t.Cleanup(func() { localDiskReserveBytes = original })

		require.False(t, ss.localDirHasSpaceFor(t.TempDir(), 0),
			"a zero-size request must still be refused when nothing is left over")
	})
}

func TestTempDirHasSpaceForUsesOSTempDir(t *testing.T) {
	ss := localDiskTestServer(t)
	require.Equal(t, ss.localDirHasSpaceFor(os.TempDir(), 1<<20), ss.tempDirHasSpaceFor(1<<20))
}

// The value of checking before create is that the client is told no before it
// sends anything, which is the difference between a rejection and a wasted
// multi-gigabyte transfer.
func TestValidateTusUploadRejectsWhenLocalDiskCannotTakeIt(t *testing.T) {
	ss := localDiskTestServer(t)

	original := localDiskReserveBytes
	localDiskReserveBytes = math.MaxUint64 - 1
	t.Cleanup(func() { localDiskReserveBytes = original })

	resp, _, err := ss.validateTusUploadBeforeCreate(handler.HookEvent{
		Upload: handler.FileInfo{
			ID:       "too-big-for-disk",
			Size:     1 << 20,
			MetaData: map[string]string{"template": string(JobTemplateAudio), "user_id": "7eP5n"},
		},
	})

	require.ErrorIs(t, err, handler.ErrUploadRejectedByServer)
	require.Equal(t, http.StatusInsufficientStorage, resp.StatusCode)
}

// A deferred length is legal tus: Size is zero and unknown, so the size-aware
// check has nothing to work with and must not read that as "needs nothing".
func TestValidateTusUploadHandlesDeferredLength(t *testing.T) {
	ss := localDiskTestServer(t)

	original := localDiskReserveBytes
	localDiskReserveBytes = math.MaxUint64 - 1
	t.Cleanup(func() { localDiskReserveBytes = original })

	resp, _, err := ss.validateTusUploadBeforeCreate(handler.HookEvent{
		Upload: handler.FileInfo{
			ID:             "deferred-length",
			SizeIsDeferred: true,
			MetaData:       map[string]string{"template": string(JobTemplateAudio), "user_id": "7eP5n"},
		},
	})

	require.ErrorIs(t, err, handler.ErrUploadRejectedByServer)
	require.Equal(t, http.StatusInsufficientStorage, resp.StatusCode)
}

// With room, the check must be invisible -- it runs before the existing
// validations and must not shadow them.
func TestValidateTusUploadPassesWhenDiskHasRoom(t *testing.T) {
	ss := localDiskTestServer(t)

	resp, _, err := ss.validateTusUploadBeforeCreate(handler.HookEvent{
		Upload: handler.FileInfo{
			ID:       "ordinary-upload",
			Size:     1 << 20,
			MetaData: map[string]string{"template": string(JobTemplateAudio), "user_id": "7eP5n"},
		},
	})

	require.NoError(t, err)
	require.Zero(t, resp.StatusCode)
}
