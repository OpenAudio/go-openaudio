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
	// so parse direclty here
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
	selectedPreview := sql.NullString{Valid: false}

	if err := validateJobTemplate(template); err != nil {
		return nil, err
	}

	var placementHosts []string = nil
	if fPlacementHosts != "" {
		placementHosts = strings.Split(fPlacementHosts, ",")
	}

	if placementHosts != nil {
		if !slices.Contains(placementHosts, ss.Config.Self.Host) {
			return nil, ErrUploadToPlacementHosts
		}
		// validate that the placement hosts are all registered nodes
		for _, host := range placementHosts {
			isRegistered := false
			for _, peer := range ss.Config.Peers {
				if peer.Host == host {
					isRegistered = true
					break
				}
			}
			if !isRegistered {
				return nil, ErrAllPlacementHostsMustBeRegisteredSigners
			}
		}
	}

	if previewStart != "" {
		previewStartSeconds, err := strconv.ParseFloat(previewStart, 64)
		if err != nil {
			return nil, ErrInvalidPreviewStartSeconds
		}
		selectedPreviewString := fmt.Sprintf("320_preview|%g", previewStartSeconds)
		selectedPreview = sql.NullString{
			Valid:  true,
			String: selectedPreviewString,
		}
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

			formFileCID, err := cidutil.ComputeFileCID(tmpFile)
			if err != nil {
				upload.Error = err.Error()
				return err
			}

			upload.OrigFileCID = formFileCID

			// ffprobe:
			upload.FFProbe, err = ffprobe(tmpFile.Name())
			if err != nil {
				// fail upload if ffprobe fails
				upload.Error = err.Error()
				return err
			}

			// ffprobe: restore orig filename
			upload.FFProbe.Format.Filename = filename

			// replicate to my bucket + others
			ss.replicateToMyBucket(ctx, formFileCID, tmpFile)
			ss.logger.Info("replicating to my bucket", zap.String("name", tmpFile.Name()), zap.String("cid", formFileCID))
			upload.Mirrors, err = ss.replicateFileParallel(ctx, formFileCID, tmpFile.Name(), placementHosts)
			if err != nil {
				upload.Error = err.Error()
				return err
			}

			ss.logger.Info("mirrored", zap.String("name", filename), zap.String("uploadID", upload.ID), zap.String("cid", formFileCID), zap.Strings("mirrors", upload.Mirrors))

			if template == JobTemplateImgSquare || template == JobTemplateImgBackdrop {
				upload.TranscodeResults["original.jpg"] = formFileCID
				upload.TranscodeProgress = 1
				upload.TranscodedAt = time.Now().UTC()
				upload.Status = JobStatusDone
				return ss.crud.Create(upload)
			}

			ss.crud.Create(upload)
			ss.transcodeWork <- upload
			return nil
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
