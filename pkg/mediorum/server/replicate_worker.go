package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
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

	// Create upload metadata
	metadata := map[string]string{
		"filename": cid,
		"filetype": "application/octet-stream",
	}

	// Create TUS upload
	client := &http.Client{
		Timeout: 10 * time.Minute,
	}

	// Step 1: Create upload
	createReq, err := http.NewRequestWithContext(ctx, "POST", tusEndpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create TUS request: %w", err)
	}

	createReq.Header.Set("Tus-Resumable", "1.0.0")
	createReq.Header.Set("Upload-Length", fmt.Sprintf("%d", fileSize))

	// Add metadata
	metadataStr := ""
	for key, val := range metadata {
		if metadataStr != "" {
			metadataStr += ","
		}
		// Encode value in base64 as per TUS spec
		encoded := base64.StdEncoding.EncodeToString([]byte(val))
		metadataStr += fmt.Sprintf("%s %s", key, encoded)
	}
	createReq.Header.Set("Upload-Metadata", metadataStr)

	createResp, err := client.Do(createReq)
	if err != nil {
		return fmt.Errorf("TUS create failed: %w", err)
	}
	defer createResp.Body.Close()

	if createResp.StatusCode != http.StatusCreated {
		return fmt.Errorf("TUS create returned status %d", createResp.StatusCode)
	}

	uploadLocation := createResp.Header.Get("Location")
	if uploadLocation == "" {
		return fmt.Errorf("TUS create response missing Location header")
	}

	// Step 2: Upload file data in chunks
	chunkSize := int64(10 * 1024 * 1024) // 10MB chunks
	offset := int64(0)

	buffer := make([]byte, chunkSize)
	for offset < fileSize {
		n, err := io.ReadFull(reader, buffer)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return fmt.Errorf("failed to read chunk: %w", err)
		}

		if n == 0 {
			break
		}

		patchReq, err := http.NewRequestWithContext(ctx, "PATCH", uploadLocation, bytes.NewReader(buffer[:n]))
		if err != nil {
			return fmt.Errorf("failed to create PATCH request: %w", err)
		}

		patchReq.Header.Set("Tus-Resumable", "1.0.0")
		patchReq.Header.Set("Upload-Offset", fmt.Sprintf("%d", offset))
		patchReq.Header.Set("Content-Type", "application/offset+octet-stream")
		patchReq.Header.Set("Content-Length", fmt.Sprintf("%d", n))

		patchResp, err := client.Do(patchReq)
		if err != nil {
			return fmt.Errorf("TUS PATCH failed at offset %d: %w", offset, err)
		}
		patchResp.Body.Close()

		if patchResp.StatusCode != http.StatusNoContent {
			return fmt.Errorf("TUS PATCH returned status %d at offset %d", patchResp.StatusCode, offset)
		}

		offset += int64(n)
		ss.logger.Debug("TUS upload progress",
			zap.String("host", host),
			zap.Int64("offset", offset),
			zap.Int64("total", fileSize),
			zap.Float64("percent", float64(offset)/float64(fileSize)*100))
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
