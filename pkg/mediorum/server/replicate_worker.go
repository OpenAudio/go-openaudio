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
	"time"

	"slices"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
	"github.com/bdragon300/tusgo"
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

			if err := ss.replicateUpload(ctx, upload); err != nil {
				logger.Warn("replication failed", zap.String("uploadID", upload.ID), zap.Error(err))
			} else {
				logger.Info("replication completed", zap.String("uploadID", upload.ID), zap.Strings("mirrors", upload.Mirrors))
			}

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (ss *MediorumServer) replicateUpload(ctx context.Context, upload *Upload) error {
	if upload.OrigFileCID == "" {
		ss.logger.Warn("replicateUpload called with empty OrigFileCID; skipping replication", zap.String("uploadID", upload.ID))
		return nil
	}
	// Get the file from our bucket
	shardedCid := cidutil.ShardCID(upload.OrigFileCID)
	attrs, err := ss.bucket.Attributes(ctx, shardedCid)
	if err != nil {
		return fmt.Errorf("failed to get file attributes: %w", err)
	}

	// Determine placement hosts
	placementHosts := upload.PlacementHosts
	if len(placementHosts) == 0 {
		placementHosts, _ = ss.rendezvousAllHosts(upload.OrigFileCID)
	}

	// Filter out self and hosts that already have the file
	targetHosts := []string{}
	for _, host := range placementHosts {
		if host == ss.Config.Self.Host {
			continue
		}
		if slices.Contains(upload.Mirrors, host) {
			continue
		}
		targetHosts = append(targetHosts, host)
	}

	if len(targetHosts) == 0 {
		ss.logger.Debug("no hosts need replication", zap.String("uploadID", upload.ID))
		return nil
	}

	// Replicate to each target host
	successHosts := []string{ss.Config.Self.Host} // Start with self
	for _, host := range targetHosts {
		// Get a fresh reader for each host
		reader, err := ss.bucket.NewReader(ctx, shardedCid, nil)
		if err != nil {
			ss.logger.Warn("failed to open file for replication",
				zap.String("host", host),
				zap.String("cid", upload.OrigFileCID),
				zap.Error(err))
			continue
		}

		err = ss.replicateViaTUS(host, upload.OrigFileCID, reader, attrs.Size)

		reader.Close()

		if err != nil {
			ss.logger.Warn("failed to replicate to host",
				zap.String("host", host),
				zap.String("cid", upload.OrigFileCID),
				zap.Error(err))
		} else {
			successHosts = append(successHosts, host)
			if len(successHosts) >= ss.Config.ReplicationFactor {
				break
			}
		}
	}

	// Update upload record with successful mirrors (don't modify the passed-in upload to avoid race conditions)
	if err := ss.crud.DB.Model(&Upload{}).Where("id = ?", upload.ID).Update("mirrors", successHosts).Error; err != nil {
		return fmt.Errorf("failed to update upload mirrors: %w", err)
	}

	ss.logger.Info("mirrored",
		zap.String("name", upload.OrigFileName),
		zap.String("uploadID", upload.ID),
		zap.String("cid", upload.OrigFileCID),
		zap.Strings("mirrors", successHosts),
	)

	return nil
}

func (ss *MediorumServer) replicateViaTUS(host string, cid string, reader io.Reader, fileSize int64) error {
	ss.logger.Info("replicating via TUS",
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
		"filename":      cid,
		"filetype":      "application/octet-stream",
		"isReplication": "true",
	}

	tusUpload := tusgo.Upload{}
	_, err = tusClient.CreateUpload(&tusUpload, fileSize, false, metadata)
	if err != nil {
		return fmt.Errorf("failed to create TUS upload: %w", err)
	}

	// Create upload stream and set chunk size to 100MB
	uploadStream := tusgo.NewUploadStream(tusClient, &tusUpload)
	uploadStream.ChunkSize = 100 * 1024 * 1024 // 100MB chunks

	// Upload the file using io.Copy
	_, err = io.Copy(uploadStream, reader)
	if err != nil {
		return fmt.Errorf("failed to upload file via TUS: %w", err)
	}

	ss.logger.Info("TUS replication completed",
		zap.String("host", host),
		zap.String("cid", cid),
		zap.Int64("size", fileSize))

	return nil
}

func (ss *MediorumServer) findMissedReplications() {
	// Find uploads that don't have enough replicas
	uploads := []*Upload{}
	ss.crud.DB.Where("status = ? AND template = 'audio'", JobStatusNew).Find(&uploads)

	for _, upload := range uploads {
		if len(upload.Mirrors) < ss.Config.ReplicationFactor {
			select {
			case ss.replicationWork <- upload:
				ss.logger.Debug("queued upload for replication", zap.String("uploadID", upload.ID))
			default:
				// Channel full, skip for now
			}
		}
	}
}
