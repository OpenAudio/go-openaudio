package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"slices"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/erni27/imcache"
	"go.uber.org/zap"
)

func (ss *MediorumServer) startReplicationWorkers(ctx context.Context) error {
	numWorkers := 3 // Run a 3 replication workers (arbitrary, can be tuned)

	ss.logger.Info("starting replication workers", zap.Int("count", numWorkers))

	// Start worker routines
	for i := range numWorkers {
		workerID := i
		go func() {
			ss.replicationWorker(ctx, workerID)
		}()
	}

	// Periodic job to find uploads that need replication
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ss.findMissedReplications()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (ss *MediorumServer) replicationWorker(ctx context.Context, workerID int) error {
	logger := ss.logger.With(zap.Int("worker", workerID), zap.String("task", "replication"))

	for {
		select {
		case upload, ok := <-ss.replicationWork:
			if !ok {
				return nil // channel closed
			}

			logger.Debug("replicating upload", zap.String("uploadID", upload.ID), zap.String("cid", upload.OrigFileCID))

			// Determine target replication count based on placement hosts
			targetReplicationCount := ss.Config.ReplicationFactor
			if len(upload.PlacementHosts) > 0 {
				targetReplicationCount = len(upload.PlacementHosts)
			}

			// Replicate transcoded file if it exists and needs replication
			if _, hasTranscoded := upload.TranscodeResults["320"]; hasTranscoded && len(upload.TranscodedMirrors) < targetReplicationCount {
				if err := ss.replicateTranscode(ctx, upload); err != nil {
					logger.Error("transcoded replication failed", zap.String("uploadID", upload.ID), zap.Error(err))
				} else {
					logger.Info("transcoded replication completed", zap.String("uploadID", upload.ID))
				}
			}

			// Replicate original file if it needs replication
			if len(upload.Mirrors) < targetReplicationCount {
				if err := ss.replicateOriginal(ctx, upload); err != nil {
					logger.Error("original replication failed", zap.String("uploadID", upload.ID), zap.Error(err))
				} else {
					logger.Info("original replication completed", zap.String("uploadID", upload.ID))
				}
			}

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (ss *MediorumServer) replicateOriginal(ctx context.Context, upload *Upload) error {
	if upload.OrigFileCID == "" {
		ss.logger.Warn("replicateUpload called with empty OrigFileCID; skipping replication", zap.String("uploadID", upload.ID))
		return nil
	}
	return ss.replicateToHosts(ctx, upload, upload.OrigFileCID, upload.Mirrors, false)
}

func (ss *MediorumServer) replicateTranscode(ctx context.Context, upload *Upload) error {
	// Get the transcoded CID from results
	transcodedCID, ok := upload.TranscodeResults["320"]
	if !ok || transcodedCID == "" {
		ss.logger.Warn("replicateTranscodedUpload called but no transcoded file exists; skipping replication", zap.String("uploadID", upload.ID))
		return nil
	}
	return ss.replicateToHosts(ctx, upload, transcodedCID, upload.TranscodedMirrors, true)
}

// replicateFile is the shared implementation for replicating files to all necessary mirrors in parallel
func (ss *MediorumServer) replicateToHosts(ctx context.Context, upload *Upload, cid string, existingMirrors []string, isTranscoded bool) error {
	// Get the file from our bucket — hot first, archive fallback so we
	// source from wherever the blob actually lives on this node.
	shardedCid := cidutil.ShardCID(cid)
	_, srcBucket, err := ss.blobAttrs(ctx, shardedCid)
	if err != nil {
		return fmt.Errorf("failed to get file attributes: %w", err)
	}

	// Determine placement hosts
	placementHosts := upload.PlacementHosts
	if len(placementHosts) == 0 {
		allHosts, _ := ss.rendezvousAllHosts(cid)
		// Limit to replication factor
		if len(allHosts) > ss.Config.ReplicationFactor {
			placementHosts = allHosts[:ss.Config.ReplicationFactor]
		} else {
			placementHosts = allHosts
		}
	}

	// Filter out self and hosts that already have the file
	targetHosts := []string{}
	for _, host := range placementHosts {
		if host == ss.Config.Self.Host {
			continue
		}
		if slices.Contains(existingMirrors, host) {
			continue
		}
		targetHosts = append(targetHosts, host)
	}

	if len(targetHosts) == 0 {
		fileType := "original"
		if isTranscoded {
			fileType = "transcoded"
		}
		ss.logger.Debug("no hosts need replication", zap.String("uploadID", upload.ID), zap.String("type", fileType))
		return nil
	}

	// Replicate to all target hosts in parallel
	type replicationResult struct {
		host string
		err  error
	}

	resultsChan := make(chan replicationResult, len(targetHosts))
	var wg sync.WaitGroup

	for _, host := range targetHosts {
		wg.Add(1)
		go func(targetHost string) {
			defer wg.Done()

			err := ss.replicateStoredFileToHost(ctx, targetHost, cid, srcBucket, shardedCid, upload.PlacementHosts, upload.ID, isTranscoded)
			resultsChan <- replicationResult{host: targetHost, err: err}
		}(host)
	}

	// Wait for all replications to complete
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results
	newSuccessHosts := []string{}
	for result := range resultsChan {
		if result.err != nil {
			fileType := "file"
			if isTranscoded {
				fileType = "transcoded file"
			}
			ss.logger.Warn("failed to replicate "+fileType+" to host",
				zap.String("host", result.host),
				zap.String("cid", cid),
				zap.Error(result.err))
		} else {
			newSuccessHosts = append(newSuccessHosts, result.host)
		}
	}

	// No replications succeeded; skip the DB read and Core operation relay.
	// The replication worker re-queues an under-replicated upload every cache
	// TTL (1h); without this fast-exit, each retry where every reachable peer
	// fails would write a fresh uploads op with byte-identical mirrors and
	// submit that op to every node via Core, dominating the
	// uploads-update op rate.
	if len(newSuccessHosts) == 0 {
		return nil
	}

	// Update upload record with successful mirrors using the operation log.
	var dbUpload Upload
	if err := ss.crud.DB.Where("id = ?", upload.ID).First(&dbUpload).Error; err != nil {
		return fmt.Errorf("failed to get upload from DB: %w", err)
	}

	// Start with existing mirrors and merge in successful hosts.
	merged, changed := mergeReplicationMirrors(isTranscoded, &dbUpload, newSuccessHosts)
	if !changed {
		// A concurrent worker has already recorded every host we just
		// replicated to. The merged list equals what's already in the DB,
		// so emitting a Core operation now would relay a row with byte-
		// identical content to every node for no semantic gain.
		ss.logger.Debug("replication produced no new mirrors; suppressing core operation",
			zap.String("uploadID", upload.ID),
			zap.String("cid", cid),
			zap.Strings("newSuccessHosts", newSuccessHosts),
		)
		return nil
	}
	if isTranscoded {
		dbUpload.TranscodedMirrors = merged
	} else {
		dbUpload.Mirrors = merged
	}

	if err := ss.crud.Update(&dbUpload); err != nil {
		return fmt.Errorf("failed to update mirrors: %w", err)
	}

	fieldName := "mirrors"
	if isTranscoded {
		fieldName = "transcoded_mirrors"
	}
	ss.logger.Info("mirrored file",
		zap.String("name", upload.OrigFileName),
		zap.String("uploadID", upload.ID),
		zap.String("cid", cid),
		zap.String("field", fieldName),
		zap.Strings(fieldName, merged),
	)

	return nil
}

// mergeReplicationMirrors merges newSuccessHosts into the upload's existing
// mirror list (transcoded vs original chosen by isTranscoded) in stable
// order, de-duplicating against hosts already present. Returns the merged
// list and whether the merge actually added anything; callers use the bool
// to decide whether to relay a Core operation or suppress a no-op write.
func mergeReplicationMirrors(isTranscoded bool, upload *Upload, newSuccessHosts []string) ([]string, bool) {
	var existing []string
	if isTranscoded {
		existing = upload.TranscodedMirrors
	} else {
		existing = upload.Mirrors
	}
	merged := append([]string{}, existing...)
	changed := false
	for _, host := range newSuccessHosts {
		if !slices.Contains(merged, host) {
			merged = append(merged, host)
			changed = true
		}
	}
	return merged, changed
}

// underReplicatedUploadsSQL selects this node's uploads that still owe a copy
// to somebody, matching what replicationWorker will actually attempt.
//
// The transcode arm is guarded on the 320 existing because the two mirror
// lists do not describe the same population. Image uploads record
// transcode_results->>'original.jpg' and never populate transcoded_mirrors at
// all, so an unguarded length check would match every image ever uploaded, on
// every pass, forever -- and the worker would drop each one right back on the
// floor, since it makes the same 320 check before doing anything.
//
// NULLIF keeps the length check total. jsonb_array_length raises on a scalar,
// and a JSON null survives COALESCE (it is a value, not SQL NULL), so a single
// legacy row storing 'null' rather than '[]' would take the error path for the
// whole query -- which is silent, because this scan feeds a background sweep
// with nobody to return an error to. Rows written by this code cannot be in
// that shape; rows arriving through crudr from other node versions are not
// ours to assume about.
const underReplicatedUploadsSQL = `created_by = ?
	AND orig_file_cid IS NOT NULL
	AND orig_file_cid != ''
	AND status != ?
	AND (
		jsonb_array_length(COALESCE(NULLIF(mirrors, 'null')::jsonb, '[]'::jsonb)) < ?
		OR (
			COALESCE(transcode_results::jsonb ->> '320', '') != ''
			AND jsonb_array_length(COALESCE(NULLIF(transcoded_mirrors, 'null')::jsonb, '[]'::jsonb)) < ?
		)
	)`

// uploadNeedsReplication reports whether either blob this upload owns is still
// short of replicas. It is the in-process twin of underReplicatedUploadsSQL and
// of the two conditions replicationWorker branches on, kept as a free function
// so the agreement between the three is directly testable.
//
// Deliberately reads replicationFactor rather than len(PlacementHosts), which
// is what the worker targets for an explicitly placed upload. That disagreement
// predates this function and is left alone here: closing it would queue a
// different population, which is a separate change from making the transcode
// shortfall visible at all.
func uploadNeedsReplication(upload *Upload, replicationFactor int) bool {
	if len(upload.Mirrors) < replicationFactor {
		return true
	}
	cid := upload.TranscodeResults["320"]
	return cid != "" && len(upload.TranscodedMirrors) < replicationFactor
}

func (ss *MediorumServer) findMissedReplications() {
	// Find uploads that don't have enough replicas
	uploads := []*Upload{}
	if err := ss.crud.DB.Where(
		underReplicatedUploadsSQL,
		ss.Config.Self.Host, JobStatusBusy, ss.Config.ReplicationFactor, ss.Config.ReplicationFactor,
	).Find(&uploads).Error; err != nil {
		// Worth a line of its own: a failure here is indistinguishable from a
		// fully replicated node, so without it the sweep can be dead for weeks
		// and look healthy the whole time.
		ss.logger.Error("failed to scan for under-replicated uploads", zap.Error(err))
		return
	}

	for _, upload := range uploads {
		if uploadNeedsReplication(upload, ss.Config.ReplicationFactor) {
			// Backoff so we don't re-queue the same upload every cycle while
			// it stays under-replicated (e.g. its source blob is gone or
			// peers keep rejecting it). After the cache TTL we'll try again.
			if _, attempted := ss.replicationAttempts.Get(upload.ID); attempted {
				continue
			}
			select {
			case ss.replicationWork <- upload:
				ss.replicationAttempts.Set(upload.ID, struct{}{}, imcache.WithDefaultExpiration())
				ss.logger.Info("queued upload for replication", zap.String("uploadID", upload.ID))
			default:
				// Channel full, skip for now
			}
		}
	}
}
