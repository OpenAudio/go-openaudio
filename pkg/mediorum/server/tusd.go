package server

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/env"
	"github.com/tus/tusd/v2/pkg/filestore"
	"github.com/tus/tusd/v2/pkg/handler"
	"go.uber.org/zap"
	"golang.org/x/exp/slog"
)

func (ss *MediorumServer) setupTusdHandler() (*handler.Handler, error) {
	// Create upload directory if it doesn't exist
	uploadDir := env.Get("/tmp/tusd-uploads", "OPENAUDIO_TUSD_UPLOAD_DIR", "TUSD_UPLOAD_DIR")

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
		Logger:                  slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})),
		NotifyCreatedUploads:    true,
		NotifyCompleteUploads:   true,
		RespectForwardedHeaders: true,
		PreUploadCreateCallback: ss.validateTusUploadBeforeCreate,
	})
	if err != nil {
		return nil, err
	}

	if tusdHandler.CreatedUploads != nil {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					ss.logger.Error("panic in tusd CreatedUploads handler", zap.Any("panic", r))
				}
			}()
			for {
				event := <-tusdHandler.CreatedUploads
				func() {
					defer func() {
						if r := recover(); r != nil {
							ss.logger.Error("panic in handleTusdUploadCreated", zap.String("id", event.Upload.ID), zap.Any("panic", r))
						}
					}()
					ss.handleTusdUploadCreated(event)
				}()
			}
		}()
	} else {
		ss.logger.Warn("tusd CreatedUploads channel is nil, upload creation events will not be handled")
	}

	// Set up post-finish hook to handle completed uploads
	if tusdHandler.CompleteUploads != nil {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					ss.logger.Error("panic in tusd CompleteUploads handler", zap.Any("panic", r))
				}
			}()
			for {
				event := <-tusdHandler.CompleteUploads
				func() {
					defer func() {
						if r := recover(); r != nil {
							ss.logger.Error("panic in handleTusdUploadComplete", zap.String("id", event.Upload.ID), zap.Any("panic", r))
						}
					}()
					ss.handleTusdUploadComplete(uploadDir, event)
				}()
			}
		}()
	} else {
		ss.logger.Warn("tusd CompleteUploads channel is nil, upload completion events will not be handled")
	}

	return tusdHandler, nil
}

func (ss *MediorumServer) validateTusUploadBeforeCreate(event handler.HookEvent) (handler.HTTPResponse, handler.FileInfoChanges, error) {

	if placementHostsStr, ok := event.Upload.MetaData["placementHosts"]; ok && placementHostsStr != "" {
		placementHosts := strings.Split(placementHostsStr, ",")
		if err := ss.validatePlacementHosts(placementHosts); err != nil {
			ss.logger.Error("placement host validation failed",
				zap.Strings("placementHosts", placementHosts),
				zap.String("self", ss.Config.Self.Host),
				zap.Error(err))
			return handler.HTTPResponse{
				StatusCode: 400,
				Body:       "invalid placement hosts: " + err.Error(),
			}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
		}
	}

	// Audio must say which user it is uploaded for, because this node later
	// attests the resulting cids to that user on chain and the attestation is
	// what entitles them to name the cids on a track. The user id is an
	// assertion, not proof — see upload_auth.go for why that is safe. Rejecting
	// an absent one here rather than at completion saves the client sending
	// bytes it can never use.
	template := JobTemplateAudio
	if t, ok := event.Upload.MetaData["template"]; ok {
		template = JobTemplate(t)
	}
	if _, err := ss.resolveUploadUserID(template, event.Upload.MetaData); err != nil {
		ss.logger.Warn("rejecting unattributed upload",
			zap.String("id", event.Upload.ID),
			zap.String("template", string(template)),
			zap.Error(err))
		return handler.HTTPResponse{
			StatusCode: 400,
			Body:       "upload attribution failed: " + err.Error(),
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	return handler.HTTPResponse{}, handler.FileInfoChanges{}, nil
}

func (ss *MediorumServer) handleTusdUploadCreated(event handler.HookEvent) {
	ss.logger.Info("tusd upload created",
		zap.String("id", event.Upload.ID),
		zap.Int64("size", event.Upload.Size),
		zap.Any("metadata", event.Upload.MetaData),
	)

	if !ss.diskHasSpace() {
		ss.logger.Warn("disk is too full to accept new uploads", zap.String("id", event.Upload.ID))
		now := time.Now().UTC()
		upload := &Upload{
			ID:        event.Upload.ID,
			Status:    JobStatusError,
			Error:     ErrDiskFull.Error(),
			CreatedBy: ss.Config.Self.Host,
			CreatedAt: now,
			UpdatedAt: now,
		}
		ss.crud.Create(upload)
		return
	}

	filename := event.Upload.MetaData["filename"]
	if filename == "" {
		filename = event.Upload.ID
	}

	// Extract and validate template from metadata
	template := JobTemplateAudio
	if templateMeta, ok := event.Upload.MetaData["template"]; ok {
		template = JobTemplate(templateMeta)
	}
	if err := validateJobTemplate(template); err != nil {
		ss.logger.Error("invalid template for tusd upload", zap.String("id", event.Upload.ID), zap.String("template", string(template)), zap.Error(err))
		now := time.Now().UTC()
		upload := &Upload{
			ID:        event.Upload.ID,
			Status:    JobStatusError,
			Error:     err.Error(),
			CreatedBy: ss.Config.Self.Host,
			CreatedAt: now,
			UpdatedAt: now,
		}
		ss.crud.Create(upload)
		return
	}

	var placementHosts []string
	if hostsStr, ok := event.Upload.MetaData["placementHosts"]; ok && hostsStr != "" {
		placementHosts = strings.Split(hostsStr, ",")
	}
	if err := ss.validatePlacementHosts(placementHosts); err != nil {
		ss.logger.Error("invalid placement hosts for tusd upload", zap.String("id", event.Upload.ID), zap.Error(err))
		now := time.Now().UTC()
		upload := &Upload{
			ID:        event.Upload.ID,
			Status:    JobStatusError,
			Error:     err.Error(),
			CreatedBy: ss.Config.Self.Host,
			CreatedAt: now,
			UpdatedAt: now,
		}
		ss.crud.Create(upload)
		return
	}

	selectedPreview := sql.NullString{Valid: false}
	if previewStart, ok := event.Upload.MetaData["previewStartSeconds"]; ok && previewStart != "" {
		parsed, err := parseSelectedPreview(previewStart)
		if err != nil {
			ss.logger.Error("invalid preview start for tusd upload", zap.String("id", event.Upload.ID), zap.Error(err))
			now := time.Now().UTC()
			upload := &Upload{
				ID:        event.Upload.ID,
				Status:    JobStatusError,
				Error:     err.Error(),
				CreatedBy: ss.Config.Self.Host,
				CreatedAt: now,
				UpdatedAt: now,
			}
			ss.crud.Create(upload)
			return
		}
		selectedPreview = parsed
	}

	// Re-read rather than carry the value over from the pre-create hook: the
	// parse is cheap, and it keeps the user id written to the row derived from
	// metadata this function saw for itself.
	userID, err := ss.resolveUploadUserID(template, event.Upload.MetaData)
	if err != nil {
		ss.logger.Error("upload attribution failed after create", zap.String("id", event.Upload.ID), zap.Error(err))
		now := time.Now().UTC()
		ss.crud.Create(&Upload{
			ID:        event.Upload.ID,
			Status:    JobStatusError,
			Error:     err.Error(),
			CreatedBy: ss.Config.Self.Host,
			CreatedAt: now,
			UpdatedAt: now,
		})
		return
	}

	now := time.Now().UTC()
	upload := &Upload{
		ID:               event.Upload.ID,
		UserID:           nullInt64(userID),
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

	filePath := filepath.Join(uploadDir, event.Upload.ID)
	infoPath := filePath + ".info"

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
	var err error

	// Retry finding the upload record with a delay (fixes test race condition, won't happen in practice as uploads take some time from create to complete)
	for attempt := 0; attempt < 3; attempt++ {
		err = ss.crud.DB.First(&upload, "id = ?", event.Upload.ID).Error
		if err == nil {
			break
		}
		if attempt < 2 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if err != nil {
		ss.logger.Error("failed to find upload record for completed tusd upload", zap.String("id", event.Upload.ID), zap.Error(err))
		return
	}

	// Skip processing if upload already failed during creation (validation errors)
	if upload.Status == JobStatusError {
		ss.logger.Warn("skipping processing for failed tusd upload", zap.String("id", event.Upload.ID), zap.String("error", upload.Error))
		return
	}

	if err := ss.processUploadedFile(ctx, upload, filePath, false); err != nil {
		ss.logger.Error("failed to process tusd upload", zap.String("id", event.Upload.ID), zap.Error(err))
		return
	}
}
