package sdk

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1storage "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1"
	storagev1connect "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1/v1connect"
	"github.com/bdragon300/tusgo"
)

type StorageServiceClient interface {
	storagev1connect.StorageServiceClient
	UploadFilesTus(context.Context, *connect.Request[v1storage.UploadFilesRequest]) (*connect.Response[v1storage.UploadFilesResponse], error)
}

type StorageServiceClientWithTUS struct {
	storagev1connect.StorageServiceClient
	tusClient *tusgo.Client
}

// UploadFilesTus implements StorageServiceClient.UploadFilesTus.
// It uploads files using the TUS resumable upload protocol.
func (s *StorageServiceClientWithTUS) UploadFilesTus(ctx context.Context, req *connect.Request[v1storage.UploadFilesRequest]) (*connect.Response[v1storage.UploadFilesResponse], error) {
	if len(req.Msg.Files) == 0 {
		return nil, fmt.Errorf("no files provided")
	}

	if len(req.Msg.Files) > 1 {
		return nil, fmt.Errorf("multiple files not supported")
	}

	file := req.Msg.Files[0]
	if file.Filename == "" {
		return nil, fmt.Errorf("filename is required")
	}

	tmpFile, err := os.CreateTemp("", "tus-upload-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := tmpFile.Write(file.Data); err != nil {
		return nil, fmt.Errorf("failed to write to temp file: %w", err)
	}

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("failed to seek temp file: %w", err)
	}

	fileInfo, err := tmpFile.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to get file info: %w", err)
	}
	fileSize := fileInfo.Size()

	uploadMeta := map[string]string{
		"filename":   file.Filename,
		"userWallet": req.Msg.UserWallet,
		"template":   req.Msg.Template,
	}
	if req.Msg.PreviewStart != "" {
		uploadMeta["previewStartSeconds"] = req.Msg.PreviewStart
	}
	if len(req.Msg.PlacementHosts) > 0 {
		uploadMeta["placementHosts"] = strings.Join(req.Msg.PlacementHosts, ",")
	}

	tusUpload := tusgo.Upload{}

	_, err = s.tusClient.CreateUpload(&tusUpload, fileSize, false, uploadMeta)
	if err != nil {
		return nil, fmt.Errorf("failed to create TUS upload: %w", err)
	}

	uploadStream := tusgo.NewUploadStream(s.tusClient, &tusUpload)
	if _, err = tmpFile.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("failed to seek file: %w", err)
	}

	if _, err = io.Copy(uploadStream, tmpFile); err != nil {
		return nil, fmt.Errorf("failed to upload file: %w", err)
	}

	if tusUpload.Location == "" {
		return nil, fmt.Errorf("no upload location returned")
	}

	// Extract ID from URL path (e.g., "http://server/files/{id}" -> "{id}")
	locURL, err := url.Parse(tusUpload.Location)
	if err != nil {
		return nil, fmt.Errorf("failed to parse upload location: %w", err)
	}

	uploadID := filepath.Base(strings.TrimRight(locURL.Path, "/"))
	if uploadID == "" || uploadID == "/" {
		return nil, fmt.Errorf("invalid upload location: %s", tusUpload.Location)
	}

	// Poll for upload CID until timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			return nil, fmt.Errorf("timeout waiting for upload CID after 10 minutes")
		case <-ticker.C:
			getUploadRes, err := s.StorageServiceClient.GetUpload(timeoutCtx, &connect.Request[v1storage.GetUploadRequest]{
				Msg: &v1storage.GetUploadRequest{
					Id: uploadID,
				},
			})
			if err != nil {
				continue // Retry on error
			}
			upload := getUploadRes.Msg.Upload
			if upload == nil {
				continue // Retry if upload not found
			}
			if upload.Error != "" {
				return nil, fmt.Errorf("upload processing failed: %s", upload.Error)
			}
			if upload.OrigFileCid != "" {
				return connect.NewResponse(&v1storage.UploadFilesResponse{
					Uploads: []*v1storage.Upload{upload},
				}), nil
			}
		}
	}
}

