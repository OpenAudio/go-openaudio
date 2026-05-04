package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
	"github.com/gabriel-vasile/mimetype"
	"go.uber.org/zap"

	"github.com/oklog/ulid/v2"
	"golang.org/x/exp/slices"
	"golang.org/x/sync/errgroup"
)

var (
	errNotFoundError      = errors.New("not found")
	errUnprocessableError = errors.New("unprocessable")

	ErrDiskFull                                 = errors.New("disk is too full to accept new uploads")
	ErrInvalidTemplate                          = errors.New("invalid template")
	ErrUploadToPlacementHosts                   = errors.New("if placement_hosts is specified, you must upload to one of the placement_hosts")
	ErrAllPlacementHostsMustBeRegisteredSigners = errors.New("all placement_hosts must be registered signers")
	ErrInvalidPreviewStartSeconds               = errors.New("invalid preview start seconds")
	ErrUploadProcessFailed                      = errors.New("upload process failed")
)

func (ss *MediorumServer) serveUpload(ctx context.Context, id string, fix bool, analyze bool) (*Upload, error) {
	var upload *Upload
	err := ss.crud.DB.First(&upload, "id = ?", id).Error
	if err != nil {
		return nil, errors.Join(errNotFoundError, err)
	}
	if upload.Status == JobStatusError {
		return upload, errors.Join(errUnprocessableError, err)
	}

	if fix && upload.Status != JobStatusDone {
		err = ss.transcode(ctx, upload)
		if err != nil {
			return nil, err
		}
	}

	if analyze && upload.AudioAnalysisStatus != "done" {
		err = ss.analyzeAudio(ctx, upload, time.Minute*10)
		if err != nil {
			return nil, err
		}
	}

	return upload, nil
}

type FileReader interface {
	Filename() string
	Open() (io.ReadCloser, error)
}

// validatePlacementHosts validates that:
// 1. If placement hosts are specified, self must be included
// 2. All placement hosts must be registered peers
func (ss *MediorumServer) validatePlacementHosts(placementHosts []string) error {
	if len(placementHosts) == 0 {
		return nil
	}

	if !slices.Contains(placementHosts, ss.Config.Self.Host) {
		return ErrUploadToPlacementHosts
	}

	for _, host := range placementHosts {
		isRegistered := false
		for _, peer := range ss.Config.Peers {
			if peer.Host == host {
				isRegistered = true
				break
			}
		}
		if !isRegistered {
			return ErrAllPlacementHostsMustBeRegisteredSigners
		}
	}

	return nil
}

// parseSelectedPreview parses previewStart string and formats it for storage.
// Returns a valid sql.NullString if previewStart is non-empty, otherwise invalid.
func parseSelectedPreview(previewStart string) (sql.NullString, error) {
	if previewStart == "" {
		return sql.NullString{Valid: false}, nil
	}

	previewStartSeconds, err := strconv.ParseFloat(previewStart, 64)
	if err != nil {
		return sql.NullString{Valid: false}, ErrInvalidPreviewStartSeconds
	}

	return sql.NullString{
		Valid:  true,
		String: fmt.Sprintf("320_preview|%g", previewStartSeconds),
	}, nil
}

// processUploadedFile handles the common file processing logic for both
// regular uploads and TUS uploads. It:
// 1. Computes the CID
// 2. Runs ffprobe
// 3. Replicates to local bucket
// 4. Queues replication to other hosts (rendezvous or placement)
// 5. Handles image templates immediately or queues audio for transcoding
//
// The upload record must already exist in the database (for TUS) or will be created (for regular uploads).
// On error, upload.Error is set and the record is updated.
func (ss *MediorumServer) processUploadedFile(ctx context.Context, upload *Upload, filePath string, shouldCreate bool) error {
	// Open the file
	tmpFile, err := os.Open(filePath)
	if err != nil {
		return ss.handleUploadError(upload, err, shouldCreate)
	}
	defer tmpFile.Close()

	// Compute CID
	formFileCID, err := cidutil.ComputeFileCID(tmpFile)
	if err != nil {
		return ss.handleUploadError(upload, err, shouldCreate)
	}
	upload.OrigFileCID = formFileCID

	// Reset file pointer for subsequent operations
	tmpFile.Seek(0, 0)

	// Run ffprobe
	upload.FFProbe, err = ffprobe(filePath)
	if err != nil {
		return ss.handleUploadError(upload, err, shouldCreate)
	}
	// Restore original filename in ffprobe result
	upload.FFProbe.Format.Filename = upload.OrigFileName

	// Replicate to my bucket
	err = ss.replicateToMyBucket(ctx, formFileCID, tmpFile, upload.PlacementHosts)
	if err != nil {
		return ss.handleUploadError(upload, err, shouldCreate)
	}
	ss.logger.Info("replicated to my bucket", zap.String("name", filePath), zap.String("cid", formFileCID))

	// Only add self to Mirrors if we're in the placement hosts (or rendezvous set if no placement hosts)
	shouldAddSelf := false
	if len(upload.PlacementHosts) > 0 {
		shouldAddSelf = slices.Contains(upload.PlacementHosts, ss.Config.Self.Host)
	} else {
		_, shouldAddSelf = ss.rendezvousAllHosts(formFileCID)
	}

	if shouldAddSelf {
		upload.Mirrors = []string{ss.Config.Self.Host}
	} else {
		// Ensure Mirrors is an empty slice rather than nil so it serializes as [] instead of null.
		upload.Mirrors = []string{}
	}

	// For images, mark as done immediately (no transcoding needed)
	if upload.Template == JobTemplateImgSquare || upload.Template == JobTemplateImgBackdrop {
		upload.TranscodeResults["original.jpg"] = formFileCID
		upload.TranscodeProgress = 1
		upload.TranscodedAt = time.Now().UTC()
		upload.Status = JobStatusDone
	}

	// Save upload record
	if shouldCreate {
		if err := ss.crud.Create(upload); err != nil {
			return err
		}
	} else {
		if err := ss.crud.Update(upload); err != nil {
			return err
		}
	}

	ss.logger.Info("upload saved, queuing for async replication",
		zap.String("name", upload.OrigFileName),
		zap.String("uploadID", upload.ID),
		zap.String("cid", formFileCID),
		zap.String("template", string(upload.Template)),
	)

	// Queue for async replication (non-blocking)
	select {
	case ss.replicationWork <- upload:
	default:
		ss.logger.Warn("replication queue full, will be picked up by periodic job", zap.String("uploadID", upload.ID))
	}

	// Queue audio for transcoding (images don't need transcoding)
	if upload.Template == JobTemplateAudio {
		ss.logger.Info("upload queued for transcode",
			zap.String("name", upload.OrigFileName),
			zap.String("uploadID", upload.ID),
			zap.String("cid", formFileCID),
			zap.String("template", string(upload.Template)))
		select {
		case ss.transcodeWork <- upload:
		default:
			ss.logger.Warn("transcode queue full, will be picked up by periodic job", zap.String("uploadID", upload.ID))
		}
	}

	return nil
}

// handleUploadError sets the error on the upload and persists it
func (ss *MediorumServer) handleUploadError(upload *Upload, err error, shouldCreate bool) error {
	upload.Error = err.Error()
	upload.Status = JobStatusError
	if shouldCreate {
		ss.crud.Create(upload)
	} else {
		if updateErr := ss.crud.Update(upload); updateErr != nil {
			ss.logger.Error("failed to update upload status", zap.String("id", upload.ID), zap.Error(updateErr))
		}
	}
	return err
}

func (ss *MediorumServer) uploadFile(ctx context.Context, qsig string, userWalletHeader string, ftemplate string, previewStart string, fPlacementHosts string, files []*multipart.FileHeader) ([]*Upload, error) {
	if !ss.diskHasSpace() {
		ss.logger.Warn("disk is too full to accept new uploads")
		return nil, ErrDiskFull
	}

	// read user wallet from ?signature query string
	// ... fall back to (legacy) X-User-Wallet header
	userWallet := sql.NullString{Valid: false}

	// updateUpload uses the requireUserSignature c.Get("signer-wallet")
	// but requireUserSignature will fail request if missing
	// so parse directly here
	if sig, err := signature.ParseFromQueryString(qsig); err == nil {
		userWallet = sql.NullString{
			String: sig.SignerWallet,
			Valid:  true,
		}
	} else {
		if userWalletHeader != "" {
			userWallet = sql.NullString{
				String: userWalletHeader,
				Valid:  true,
			}
		}
	}

	template := JobTemplate(ftemplate)

	if err := validateJobTemplate(template); err != nil {
		return nil, err
	}

	var placementHosts []string = nil
	if fPlacementHosts != "" {
		placementHosts = strings.Split(fPlacementHosts, ",")
	}

	if err := ss.validatePlacementHosts(placementHosts); err != nil {
		return nil, err
	}

	selectedPreview, err := parseSelectedPreview(previewStart)
	if err != nil {
		return nil, err
	}

	// each file:
	// - hash contents
	// - send to server in hashring for processing
	// - some task queue stuff

	uploads := make([]*Upload, len(files))
	wg, _ := errgroup.WithContext(ctx)
	for idx, formFile := range files {

		idx := idx
		formFile := formFile
		ss.logger.Info("formFile", zap.String("contentType", formFile.Header.Get("Content-Type")))

		wg.Go(func() error {
			now := time.Now().UTC()
			filename := formFile.Filename
			upload := &Upload{
				ID:               ulid.Make().String(),
				UserWallet:       userWallet,
				Status:           JobStatusNew,
				Template:         template,
				SelectedPreview:  selectedPreview,
				CreatedBy:        ss.Config.Self.Host,
				CreatedAt:        now,
				UpdatedAt:        now,
				OrigFileName:     filename,
				TranscodeResults: map[string]string{},
				PlacementHosts:   placementHosts,
			}
			uploads[idx] = upload

			tmpFile, err := copyUploadToTempFile(formFile)
			if err != nil {
				upload.Error = err.Error()
				return err
			}
			defer os.Remove(tmpFile.Name())

			// Use shared processing logic
			return ss.processUploadedFile(ctx, upload, tmpFile.Name(), true)
		})
	}

	if err := wg.Wait(); err != nil {
		ss.logger.Error("failed to process new upload", zap.Error(err))
		return nil, fmt.Errorf("failed to process new upload: %w", err)
	}

	return uploads, nil
}

func (ss *MediorumServer) createMultipartFileHeader(filename string, data []byte) (*multipart.FileHeader, error) {
	// Infer MIME type from first 512 bytes
	mtype := mimetype.Detect(data)
	ct := mtype.String()

	// Fallback to extension-based MIME if detection failed or is too generic
	if ct == "application/octet-stream" {
		ext := filepath.Ext(filename)
		mimeByExt := mime.TypeByExtension(ext)
		if mimeByExt != "" {
			ct = mimeByExt
		}
	}

	// Create a buffer to write the multipart data into
	var b bytes.Buffer
	w := multipart.NewWriter(&b)

	// Create a form file part
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}

	// Write the file data into the multipart part
	if _, err := part.Write(data); err != nil {
		return nil, err
	}

	// Close the writer to finalize the form
	if err := w.Close(); err != nil {
		return nil, err
	}

	// Now parse the multipart data as if it came from an HTTP request
	req := &http.Request{
		Header: make(http.Header),
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Body = io.NopCloser(bytes.NewReader(b.Bytes()))
	req.ContentLength = int64(b.Len())

	if err := req.ParseMultipartForm(int64(b.Len())); err != nil {
		return nil, err
	}

	// Grab the file header
	fileHeaders := req.MultipartForm.File["file"]
	if len(fileHeaders) == 0 {
		return nil, fmt.Errorf("no file found in multipart form")
	}

	// Set content-type in the header explicitly since multipart doesn't infer it
	fileHeaders[0].Header.Set("Content-Type", ct)

	return fileHeaders[0], nil
}
