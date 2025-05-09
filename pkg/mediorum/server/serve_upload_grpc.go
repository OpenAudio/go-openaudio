package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AudiusProject/audiusd/pkg/mediorum/cidutil"
	"github.com/AudiusProject/audiusd/pkg/mediorum/server/signature"

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

func (ss *MediorumServer) serveUpload(id string, fix bool, analyze bool) (*Upload, error) {
	var upload *Upload
	err := ss.crud.DB.First(&upload, "id = ?", id).Error
	if err != nil {
		return nil, errors.Join(errNotFoundError, err)
	}
	if upload.Status == JobStatusError {
		return upload, errors.Join(errUnprocessableError, err)
	}

	if fix && upload.Status != JobStatusDone {
		err = ss.transcode(upload)
		if err != nil {
			return nil, err
		}
	}

	if analyze && upload.AudioAnalysisStatus != "done" {
		err = ss.analyzeAudio(upload, time.Minute*10)
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

func (ss *MediorumServer) uploadFile(ctx context.Context, qsig string, userWalletHeader string, ftemplate string, previewStart string, fPlacementHosts string, files []FileReader) ([]*Upload, error) {
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
		wg.Go(func() error {
			now := time.Now().UTC()
			filename := formFile.Filename()
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

			tmpFile, err := copyTempFile(formFile)
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
			ss.replicateToMyBucket(formFileCID, tmpFile)
			upload.Mirrors, err = ss.replicateFileParallel(formFileCID, tmpFile.Name(), placementHosts)
			if err != nil {
				upload.Error = err.Error()
				return err
			}

			ss.logger.Info("mirrored", "name", filename, "uploadID", upload.ID, "cid", formFileCID, "mirrors", upload.Mirrors)

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
		ss.logger.Error("failed to process new upload", "err", err)
		return nil, ErrUploadProcessFailed
	}

	return uploads, nil
}

func copyTempFile(file FileReader) (*os.File, error) {
	temp, err := os.CreateTemp("", "mediorumUpload")
	if err != nil {
		return nil, err
	}

	r, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()

	_, err = io.Copy(temp, r)
	if err != nil {
		return nil, err
	}
	temp.Sync()
	temp.Seek(0, 0)

	return temp, nil
}
