package server

import (
	"os"

	"github.com/OpenAudio/go-openaudio/pkg/env"
	"go.uber.org/zap"
)

// Local disk is not the blob store, and on most nodes it is not even the same
// filesystem. diskHasSpace measures BlobStoreDSN, and dsnHasSpace returns true
// for anything that is not a file:// URL -- so on an S3, GCS or R2 backed node
// every upload-path disk check passes unconditionally while the bytes land on a
// local filesystem nothing is looking at.
//
// That filesystem is where an upload actually goes first: tusd writes the
// incoming body under its upload dir, the multipart handler spills to the OS
// temp dir, transcode and audio analysis stage there too. Filling it does not
// just fail the upload that filled it -- it takes out every other user of the
// directory until something drains.
//
// The measurement already exists: startup monitoring statfs's Config.Dir
// precisely when the blob store is not file:// (see monitor.go), so
// mediorumPathFree is a live reading of local disk on exactly the nodes where
// nothing consults it. These checks are what consult it.

// localDiskReserveBytes is what must remain free after the upload being
// admitted.
//
// Its one job is to stop us handing out the last of the disk. With an exact
// size check and no reserve, "it fits" and "it fills the volume to zero" are
// the same admission, and zero is qualitatively worse than a rejected upload:
// postgres on the same volume goes read-only, logging stops, and metadata
// operations start failing across everything else sharing it.
//
// It is explicitly not concurrency protection, which is the reading a bigger
// number would invite. Transfers already writing show up in statfs on their
// own; two admitted in the same instant do not, and no reserve fixes that --
// on 8GB free, two 5GB uploads arriving together both pass whatever margin is
// set, because neither has written yet. Only reservation accounting would, and
// for tus that means expiring the reservations of abandoned uploads.
//
// So it is sized for "leave the machine working", which is roughly the job
// ext4's own 5% root reserve does. Worth noting getDiskStatus reads Bfree
// rather than Bavail, so that filesystem reserve is currently counted as free
// space here; reading Bavail would let this shrink further or go away.
//
// A var only so tests can force the refusal branch; nothing reassigns it at
// runtime.
var localDiskReserveBytes uint64 = 1 << 30

// tusdUploadDir is where tusd stores in-progress uploads.
func tusdUploadDir() string {
	return env.Get("/tmp/tusd-uploads", "OPENAUDIO_TUSD_UPLOAD_DIR", "TUSD_UPLOAD_DIR")
}

// pullStagingDir is where an inbound pull buffers a blob before it is validated
// and committed to the bucket -- the same class of local staging as the two
// above, and gated by the same helper.
//
// Defaults to the OS temp dir, which on a container deployment is the image's
// root filesystem rather than the blob volume -- see ensureNoTmpDir, which
// exists because those are routinely different mount points. Operators whose
// root filesystem is small can point this at the volume that actually has the
// room; on a file:// node, pointing it at the blob store means the existing
// diskHasSpaceForCID check covers staging too.
func (ss *MediorumServer) pullStagingDir() string {
	if ss.Config.PullStagingDir != "" {
		return ss.Config.PullStagingDir
	}
	return os.TempDir()
}

// localDirHasSpaceFor reports whether dir's filesystem can take needBytes and
// still leave localDiskReserveBytes behind.
//
// needBytes is the size the caller already knows -- a tus Upload-Length or a
// request Content-Length -- so this is an exact question rather than a
// threshold, and the answer can be given before a single byte is accepted.
// Pass 0 when the size is genuinely unknown; the reserve alone still catches a
// disk that is already full.
//
// A statfs failure allows the upload rather than refusing it, matching what
// dsnHasSpace does: an unreadable mount is not evidence of a full one, and
// refusing every upload on it would be a worse failure than the one being
// guarded.
func (ss *MediorumServer) localDirHasSpaceFor(dir string, needBytes uint64) bool {
	if ss.Config.Env != "prod" {
		return true
	}

	_, free, err := getDiskStatus(dir)
	if err != nil {
		if ss.diskWarnThrottle.allow("local-statfs-failed:"+dir, diskWarnInterval) {
			ss.logger.Warn("failed to check local disk space; accepting uploads unchecked",
				zap.String("dir", dir),
				zap.Error(err))
		}
		return true
	}

	// Both orderings of this comparison have an unsigned trap, and they fail in
	// opposite directions. free-needBytes underflows when the request is larger
	// than the disk, and needBytes+reserve overflows when the request is near
	// the top of the range -- and overflow is the dangerous one, wrapping to a
	// small number that reads as ample room. Rule out the oversized request
	// first, which makes the subtraction safe.
	if needBytes > free || free-needBytes <= localDiskReserveBytes {
		if ss.diskWarnThrottle.allow("local-below-threshold:"+dir, diskWarnInterval) {
			ss.logger.Warn("local disk cannot take this upload; refusing",
				zap.String("dir", dir),
				zap.Uint64("freeBytes", free),
				zap.Uint64("needBytes", needBytes),
				zap.Uint64("reserveBytes", localDiskReserveBytes))
		}
		return false
	}
	return true
}

// tempDirHasSpaceFor is localDirHasSpaceFor against the OS temp dir, where the
// multipart handler and the transcode and analysis stages write.
func (ss *MediorumServer) tempDirHasSpaceFor(needBytes uint64) bool {
	return ss.localDirHasSpaceFor(os.TempDir(), needBytes)
}
