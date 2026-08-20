package server

import (
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"slices"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
	"github.com/bdragon300/tusgo"
	"github.com/erni27/imcache"
	"go.uber.org/zap"
)

// tusAuthTransport adds authentication headers to TUS requests for replication
type tusAuthTransport struct {
	base       http.RoundTripper
	privateKey *ecdsa.PrivateKey
	selfHost   string
}

func (t *tusAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Add basic auth signature (same as non-TUS replication)
	if t.privateKey != nil {
		ts := fmt.Sprintf("%d", time.Now().UnixMilli())
		sig, err := signature.Sign(ts, t.privateKey)
		if err != nil {
			return nil, fmt.Errorf("failed to sign request: %w", err)
		}
		signatureHex := fmt.Sprintf("0x%s", hex.EncodeToString(sig))
		basic := ts + ":" + signatureHex
		auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(basic))
		req.Header.Set("Authorization", auth)
		req.Header.Set("User-Agent", "mediorum "+t.selfHost)
	} else {
		// Dev mode
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("dev:mode")))
	}
	return t.base.RoundTrip(req)
}

func (ss *MediorumServer) startReplicationWorkers(ctx context.Context) error {
	// Individual transfers are no longer capped at three minutes, so a single
	// long blob can occupy a worker for far longer than before. More workers
	// keep the queue draining while one is busy with a large upload.
	numWorkers := 6

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

			err := ss.replicateStoredFileToHost(ctx, targetHost, cid, srcBucket, shardedCid, upload.PlacementHosts)
			// TODO: Replicate with TUSD
			// err = ss.replicateToHost(targetHost, cid, reader, attrs.Size, placementHosts)
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

func (ss *MediorumServer) replicateToHost(host string, cid string, reader io.Reader, fileSize int64, placementHosts []string) error {
	ss.logger.Info("replicating via TUSD",
		zap.String("host", host),
		zap.String("cid", cid),
		zap.Int64("size", fileSize))

	// TUS upload endpoint
	tusEndpoint := host + "/files/"
	tusBaseURL, err := url.Parse(tusEndpoint)
	if err != nil {
		return fmt.Errorf("failed to parse TUS endpoint URL: %w", err)
	}

	// Create authenticated HTTP client with signature transport
	authTransport := &tusAuthTransport{
		base:       http.DefaultTransport,
		privateKey: ss.Config.privateKey,
		selfHost:   ss.Config.Self.Host,
	}
	httpClient := &http.Client{
		Timeout:   10 * time.Minute,
		Transport: authTransport,
	}

	tusClient := tusgo.NewClient(httpClient, tusBaseURL)
	tusClient.Capabilities = &tusgo.ServerCapabilities{
		Extensions:       []string{"creation", "creation-with-upload", "termination"},
		ProtocolVersions: []string{"1.0.0"},
	}

	// Create upload with metadata - mark as replication to skip processing
	metadata := map[string]string{
		"filename":       cid,
		"filetype":       "application/octet-stream",
		"isReplication":  "true",
		"placementHosts": strings.Join(placementHosts, ","),
	}

	tusUpload := tusgo.Upload{}
	_, err = tusClient.CreateUpload(&tusUpload, fileSize, false, metadata)
	if err != nil {
		return fmt.Errorf("failed to create TUSD upload: %w", err)
	}

	// Create upload stream and set chunk size to 100MB
	uploadStream := tusgo.NewUploadStream(tusClient, &tusUpload)
	uploadStream.ChunkSize = 100 * 1000 * 1000 // 100MB chunks

	// Upload the file using io.Copy
	_, err = io.Copy(uploadStream, reader)
	if err != nil {
		return fmt.Errorf("failed to upload file via TUSD: %w", err)
	}

	ss.logger.Info("TUSD replication completed",
		zap.String("host", host),
		zap.String("cid", cid),
		zap.Int64("size", fileSize))

	return nil
}

func (ss *MediorumServer) findMissedReplications() {
	// Find uploads that don't have enough replicas
	uploads := []*Upload{}
	ss.crud.DB.Where(
		"created_by = ? AND orig_file_cid IS NOT NULL AND orig_file_cid != '' AND status != ? AND jsonb_array_length(COALESCE(mirrors::jsonb, '[]'::jsonb)) < ?",
		ss.Config.Self.Host, JobStatusBusy, ss.Config.ReplicationFactor,
	).Find(&uploads)

	for _, upload := range uploads {
		if len(upload.Mirrors) < ss.Config.ReplicationFactor {
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
