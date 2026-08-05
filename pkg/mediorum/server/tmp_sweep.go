package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/persistence"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
	"gocloud.dev/blob"
)

// Orphaned fileblob ".tmp" files
//
// fileblob writes to "<path>.<nanos>.tmp" and renames on a successful Close, so
// a surviving ".tmp" is an interrupted write. writerWithSidecar.Close always
// removes its own temp unless the rename succeeded, and replicateToMyBucket
// closes the writer on the copy-error path too — so every graceful failure
// (cancelled context, HTTP timeout, disk error, peer hangup) cleans up after
// itself. Orphans only survive a hard kill: SIGKILL, OOM, power loss.
//
// That bounds the population at roughly (ungraceful restarts x concurrent
// writers) — tens of files over a node's lifetime, each at most one blob.
//
// This used to be swept by a full recursive walk inside persistence.Open,
// synchronously, before mediorum could initialize. On a store-all archive with
// ~1M objects across ~1.4M single-file directories that walk takes about an
// hour, during which the node binds no port, serves no content and answers no
// storage proofs. An hour of downtime per restart to reclaim a handful of files
// is a bad trade at any scheduling.
//
// Instead: cleanupStaleTempsNearKey removes them opportunistically as a side
// effect of writing, which lands exactly where orphans accumulate (a write that
// was interrupted left the key missing, so repair retries it into the same
// directory). A full sweep remains available on demand for the rare case where
// disk accounting justifies it.

// startTmpSweeper runs a full stale-temp sweep whenever one is requested via
// the internal API. It is a managed routine so the sweep inherits the
// lifecycle's context and stops promptly on shutdown.
func (ss *MediorumServer) startTmpSweeper(ctx context.Context) error {
	for {
		select {
		case <-ss.tmpSweepTrigger:
			ss.sweepStaleTempFiles(ctx)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// sweepStaleTempFiles walks every file:// bucket and removes orphaned ".tmp"
// files older than persistence.DefaultStaleTempFileAge. This is the expensive
// path — a full recursive traversal per bucket — and is only reached on an
// explicit operator request.
func (ss *MediorumServer) sweepStaleTempFiles(ctx context.Context) {
	dsns := []string{ss.Config.BlobStoreDSN}
	if ss.Config.ArchiveBlobStoreDSN != "" {
		dsns = append(dsns, ss.Config.ArchiveBlobStoreDSN)
	}

	for _, dsn := range dsns {
		if ctx.Err() != nil {
			return
		}
		dir, isFile := persistence.FileDirFromDSN(dsn)
		if !isFile {
			// Cloud backends have no local tree. Their equivalent artifact is
			// an incomplete multipart upload, which isn't visible in the
			// keyspace at all and is handled by a bucket lifecycle rule.
			continue
		}

		logger := ss.logger.With(zap.String("task", "tmpSweep"), zap.String("dir", dir))
		logger.Info("full stale temp file sweep starting")

		start := time.Now()
		removed, err := persistence.SweepStaleTempFiles(ctx, dsn, persistence.DefaultStaleTempFileAge)
		if err != nil {
			logger.Warn("full stale temp file sweep failed",
				zap.Int("removed", removed), zap.Duration("took", time.Since(start)), zap.Error(err))
			continue
		}
		logger.Info("full stale temp file sweep complete",
			zap.Int("removed", removed), zap.Duration("took", time.Since(start)))
	}
}

// cleanupStaleTempsNearKey removes orphaned ".tmp" files from the directory a
// blob was just written into. Called after every successful local write.
//
// The directory is already hot from the write and holds only a handful of
// entries, so this is a cheap readdir. It self-targets: an interrupted write
// leaves both an orphan and a still-missing key, and repair's retry writes that
// key back into the same directory.
//
// The age filter is not about this write — the temp for it has already been
// renamed away — but about concurrent writers, which at RepairConcurrency > 1
// may legitimately hold a live ".tmp" in the same directory.
func (ss *MediorumServer) cleanupStaleTempsNearKey(bucket *blob.Bucket, key string) {
	root, isFile := persistence.FileDirFromDSN(ss.dsnForBucket(bucket))
	if !isFile {
		return
	}

	dir := filepath.Dir(filepath.Join(root, filepath.FromSlash(key)))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-persistence.DefaultStaleTempFileAge)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := os.Remove(path); err == nil {
			ss.logger.Debug("removed orphaned temp file", zap.String("path", path))
		}
	}
}

// dsnForBucket maps an open bucket back to the DSN it was opened from, so
// callers can resolve filesystem paths for file:// backends.
func (ss *MediorumServer) dsnForBucket(bucket *blob.Bucket) string {
	if ss.archiveBucket != nil && bucket == ss.archiveBucket {
		return ss.Config.ArchiveBlobStoreDSN
	}
	return ss.Config.BlobStoreDSN
}

// serveTmpSweep queues a full stale-temp sweep. The trigger channel has a
// capacity of one, so a request while a sweep is already running or queued is
// rejected rather than piling up traversals.
func (ss *MediorumServer) serveTmpSweep(c echo.Context) error {
	select {
	case ss.tmpSweepTrigger <- struct{}{}:
		return c.JSON(http.StatusAccepted, map[string]string{
			"status": "sweep queued; results are logged with task=tmpSweep",
		})
	default:
		return c.JSON(http.StatusConflict, map[string]string{
			"error": "a sweep is already queued or running",
		})
	}
}
