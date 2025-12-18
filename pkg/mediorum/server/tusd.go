package server

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"

	"github.com/tus/tusd/v2/pkg/filestore"
	"github.com/tus/tusd/v2/pkg/handler"
	"go.uber.org/zap"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
)

func (ss *MediorumServer) setupTusdHandler() (*handler.Handler, error) {
	// Create upload directory if it doesn't exist
	uploadDir := os.Getenv("TUSD_UPLOAD_DIR")
	if uploadDir == "" {
		uploadDir = "/tmp/tusd-uploads"
	}

	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return nil, err
	}

	ss.logger.Info("setting up tusd handler", zap.String("uploadDir", uploadDir))

	// Create file store for tusd
	store := filestore.New(uploadDir)

	// Create tusd composer
	composer := handler.NewStoreComposer()
	store.UseIn(composer)

	// Create tusd handler
	tusdHandler, err := handler.NewHandler(handler.Config{
		BasePath:                "/files/",
		StoreComposer:           composer,
		DisableDownload:         true,
		NotifyCreatedUploads:    true,
		NotifyCompleteUploads:   true,
		RespectForwardedHeaders: true,
	})
	if err != nil {
		return nil, err
	}

	go func() {
		for {
			event := <-tusdHandler.CreatedUploads
			ss.handleTusdUploadCreated(event)
		}
	}()

	// Set up post-finish hook to handle completed uploads
	go func() {
		for {
			event := <-tusdHandler.CompleteUploads
			ss.handleTusdUploadComplete(uploadDir, event)
		}
	}()

	return tusdHandler, nil
}

func (ss *MediorumServer) handleTusdUploadCreated(event handler.HookEvent) {
	ss.logger.Info("tusd upload created",
		zap.String("id", event.Upload.ID),
		zap.Int64("size", event.Upload.Size),
		zap.Any("metadata", event.Upload.MetaData),
	)

	// Extract metadata
	filename := event.Upload.MetaData["filename"]
	if filename == "" {
		filename = event.Upload.ID
	}

	// Extract user wallet from metadata
	userWallet := sql.NullString{Valid: false}
	if wallet, ok := event.Upload.MetaData["userWallet"]; ok && wallet != "" {
		userWallet = sql.NullString{String: wallet, Valid: true}
	}

	// Extract template from metadata
	template := JobTemplateAudio // default to audio
	if templateMeta, ok := event.Upload.MetaData["template"]; ok {
		template = JobTemplate(templateMeta)
	}

	// Extract preview settings from metadata
	selectedPreview := sql.NullString{Valid: false}
	if previewStart, ok := event.Upload.MetaData["previewStartSeconds"]; ok && previewStart != "" {
		selectedPreview = sql.NullString{Valid: true, String: previewStart}
	}

	// Extract placement hosts from metadata
	var placementHosts []string
	if hostsStr, ok := event.Upload.MetaData["placementHosts"]; ok && hostsStr != "" {
		placementHosts = append(placementHosts, hostsStr)
	}

	now := time.Now().UTC()
	upload := &Upload{
		ID:               event.Upload.ID,
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
	if err := ss.crud.Create(upload); err != nil {
		ss.logger.Error("failed to create upload record for tusd upload", zap.String("id", event.Upload.ID), zap.Error(err))
		return
	}
}

func (ss *MediorumServer) handleTusdUploadComplete(uploadDir string, event handler.HookEvent) {
	ctx := context.Background()

	ss.logger.Info("tusd upload completed",
		zap.String("id", event.Upload.ID),
		zap.Int64("size", event.Upload.Size),
		zap.Any("metadata", event.Upload.MetaData),
	)

	// Construct file path
	filePath := filepath.Join(uploadDir, event.Upload.ID)
	infoPath := filePath + ".info"

	// Ensure we clean up the files after processing
	defer func() {
		if err := os.Remove(filePath); err != nil {
			ss.logger.Warn("failed to remove tusd upload file", zap.String("path", filePath), zap.Error(err))
		}
		if err := os.Remove(infoPath); err != nil {
			ss.logger.Warn("failed to remove tusd info file", zap.String("path", infoPath), zap.Error(err))
		}
	}()

	// Load upload record
	var upload *Upload
	err := ss.crud.DB.First(&upload, "id = ?", event.Upload.ID).Error
	if err != nil {
		ss.logger.Error("failed to find upload record for completed tusd upload", zap.String("id", event.Upload.ID), zap.Error(err))
		return
	}

	filename := upload.OrigFileName
	template := upload.Template
	placementHosts := upload.PlacementHosts

	// Open the uploaded file
	tmpFile, err := os.Open(filePath)
	if err != nil {
		upload.Error = err.Error()
		ss.logger.Error("failed to open tusd upload file", zap.Error(err))
		return
	}
	defer tmpFile.Close()

	// Compute CID
	formFileCID, err := cidutil.ComputeFileCID(tmpFile)
	if err != nil {
		upload.Error = err.Error()
		ss.logger.Error("failed to compute CID", zap.Error(err))
		return
	}
	upload.OrigFileCID = formFileCID

	// Reset file pointer for subsequent operations
	tmpFile.Seek(0, 0)

	// Run ffprobe
	upload.FFProbe, err = ffprobe(filePath)
	if err != nil {
		upload.Error = err.Error()
		ss.logger.Error("ffprobe failed", zap.Error(err))
		return
	}
	upload.FFProbe.Format.Filename = filename

	// Replicate to my bucket + others
	ss.replicateToMyBucket(ctx, formFileCID, tmpFile)
	upload.Mirrors, err = ss.replicateFileParallel(ctx, formFileCID, filePath, placementHosts)
	if err != nil {
		upload.Error = err.Error()
		ss.logger.Error("failed to replicate file", zap.Error(err))
		return
	}

	ss.logger.Info("mirrored tusd upload",
		zap.String("name", filename),
		zap.String("uploadID", upload.ID),
		zap.String("cid", formFileCID),
		zap.Strings("mirrors", upload.Mirrors),
	)

	// Handle image uploads immediately
	if template == JobTemplateImgSquare || template == JobTemplateImgBackdrop {
		upload.TranscodeResults["original.jpg"] = formFileCID
		upload.TranscodeProgress = 1
		upload.TranscodedAt = time.Now().UTC()
		upload.Status = JobStatusDone
		if err := ss.crud.Update(upload); err != nil {
			ss.logger.Error("failed to update upload", zap.Error(err))
		}
		return
	}

	// Update upload record and queue for transcoding
	if err := ss.crud.Update(upload); err != nil {
		ss.logger.Error("failed to update upload", zap.Error(err))
		return
	}

	ss.transcodeWork <- upload
}
