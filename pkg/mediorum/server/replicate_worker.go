package server

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/bdragon300/tusgo"
	"go.uber.org/zap"
)

const (
	// Use TUS for files larger than 100MB
	tusThresholdBytes = 100 * 1024 * 1024
)

func (ss *MediorumServer) startReplicationWorkers(ctx context.Context) error {
	numWorkers := 3 // Run 3 parallel replication workers

	ss.logger.Info("starting replication workers", zap.Int("count", numWorkers))

	// Start worker routines
	for i := 0; i < numWorkers; i++ {
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
		if contains(upload.Mirrors, host) {
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

		// For large files, use TUS; for small files, use regular HTTP POST
		if attrs.Size > tusThresholdBytes {
			err = ss.replicateViaTUS(ctx, host, upload.OrigFileCID, reader, attrs.Size)
		} else {
			err = ss.replicateFileToHost(ctx, host, upload.OrigFileCID, reader)
		}
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

	// Update upload record with successful mirrors
	upload.Mirrors = successHosts
	if err := ss.crud.Update(upload); err != nil {
		return fmt.Errorf("failed to update upload mirrors: %w", err)
	}

	ss.logger.Info("mirrored",
		zap.String("name", upload.OrigFileName),
		zap.String("uploadID", upload.ID),
		zap.String("cid", upload.OrigFileCID),
		zap.Strings("mirrors", upload.Mirrors),
	)

	return nil
}

func (ss *MediorumServer) replicateViaTUS(ctx context.Context, host string, cid string, reader io.Reader, fileSize int64) error {
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

	// Create TUS client
	httpClient := &http.Client{
		Timeout: 10 * time.Minute,
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

// Helper function to check if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// replicateFileToHostMultipart replicates a file using multipart upload with extended timeout
func (ss *MediorumServer) replicateFileToHostMultipart(ctx context.Context, peer string, fileName string, file io.Reader, fileSize int64) error {
	if peer == ss.Config.Self.Host {
		return ss.replicateToMyBucket(ctx, fileName, file)
	}

	// Calculate timeout based on file size: 1 minute per 100MB, minimum 2 minutes
	timeout := time.Duration(fileSize/(100*1024*1024)+2) * time.Minute
	if timeout < 2*time.Minute {
		timeout = 2 * time.Minute
	}
	if timeout > 30*time.Minute {
		timeout = 30 * time.Minute
	}

	client := http.Client{
		Timeout: timeout,
	}

	r, w := io.Pipe()
	m := multipart.NewWriter(w)
	errChan := make(chan error)

	go func() {
		defer w.Close()
		defer m.Close()
		part, err := m.CreateFormFile(filesFormFieldName, fileName)
		if err != nil {
			errChan <- err
			return
		}
		if _, err = io.Copy(part, file); err != nil {
			errChan <- err
			return
		}
		close(errChan)
	}()

	// Create signed POST request
	req, err := http.NewRequestWithContext(ctx, "POST", peer+"/internal/blobs", r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", m.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("replication returned status %d", resp.StatusCode)
	}

	return <-errChan
}
