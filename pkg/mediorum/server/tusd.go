package server

import (
	"context"
	"database/sql"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/hashes"
	"github.com/ipfs/go-cid"
	"github.com/tus/tusd/v2/pkg/filestore"
	"github.com/tus/tusd/v2/pkg/handler"
	"go.uber.org/zap"
	"golang.org/x/exp/slices"
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

	placementHostsStr, hasPlacement := event.Upload.MetaData["placementHosts"]
	placementHosts := []string{}
	if hasPlacement && placementHostsStr != "" {
		placementHosts = strings.Split(placementHostsStr, ",")
		err := ss.validatePlacementHosts(placementHosts)
		if err != nil {
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

	// Check if this is a replication request
	isReplication, ok := event.Upload.MetaData["isReplication"]
	if !ok || isReplication != "true" {
		// Not a replication - allow (user uploads don't require auth)
		return handler.HTTPResponse{}, handler.FileInfoChanges{}, nil
	}

	// Replication requests must be authenticated
	authHeader := ""
	if event.HTTPRequest.Header != nil {
		authHeader = event.HTTPRequest.Header.Get("Authorization")
	}

	if authHeader == "" {
		ss.logger.Warn("replication upload missing authentication",
			zap.String("id", event.Upload.ID),
			zap.String("filename", event.Upload.MetaData["filename"]))
		return handler.HTTPResponse{
			StatusCode: 401,
			Body:       "replication upload missing authentication",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	// Parse Basic auth
	if !strings.HasPrefix(authHeader, "Basic ") {
		ss.logger.Warn("invalid auth header format",
			zap.String("id", event.Upload.ID),
			zap.String("authHeader", authHeader))
		return handler.HTTPResponse{
			StatusCode: 401,
			Body:       "invalid auth header format",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	decoded, err := base64.StdEncoding.DecodeString(authHeader[6:])
	if err != nil {
		ss.logger.Warn("failed to decode auth header",
			zap.String("id", event.Upload.ID),
			zap.Error(err))
		return handler.HTTPResponse{
			StatusCode: 401,
			Body:       "failed to decode auth header",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	parts := strings.Split(string(decoded), ":")
	if len(parts) != 2 {
		ss.logger.Warn("invalid auth format",
			zap.String("id", event.Upload.ID),
			zap.Int("parts", len(parts)))
		return handler.HTTPResponse{
			StatusCode: 401,
			Body:       "invalid auth format",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	user, pass := parts[0], parts[1]

	// Validate password format before calling checkBasicAuth to prevent panic
	if !strings.HasPrefix(pass, "0x") || len(pass) <= 2 {
		ss.logger.Warn("invalid password format",
			zap.String("id", event.Upload.ID),
			zap.String("user", user))
		return handler.HTTPResponse{
			StatusCode: 401,
			Body:       "invalid password format",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	ok, err = ss.checkBasicAuth(user, pass, nil)
	if !ok {
		ss.logger.Warn("basic auth check failed",
			zap.String("id", event.Upload.ID),
			zap.String("user", user),
			zap.Error(err))
		return handler.HTTPResponse{
			StatusCode: 401,
			Body:       "authentication failed",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	// Validate CID format (filename should be a valid CID for replication)
	filename := event.Upload.MetaData["filename"]
	if filename == "" {
		ss.logger.Warn("replication upload missing filename (CID)",
			zap.String("id", event.Upload.ID))
		return handler.HTTPResponse{
			StatusCode: 400,
			Body:       "replication upload missing filename (CID)",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	// Parse CID to ensure it's valid
	_, err = cid.Decode(filename)
	if err != nil {
		ss.logger.Warn("invalid CID format",
			zap.String("id", event.Upload.ID),
			zap.String("filename", filename),
			zap.Error(err))
		return handler.HTTPResponse{
			StatusCode: 400,
			Body:       "invalid CID format",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	// Check if this node should store this CID (based on rendezvous hashing or placement hosts)
	var shouldStore bool

	if hasPlacement && placementHostsStr != "" {
		// If placement hosts are specified, check if we're in the list
		shouldStore = slices.Contains(placementHosts, ss.Config.Self.Host)
	} else {
		// Otherwise use rendezvous hashing
		_, shouldStore = ss.rendezvousAllHosts(filename)
	}

	if !shouldStore {
		ss.logger.Warn("this node is not a placement host for the given CID",
			zap.String("id", event.Upload.ID),
			zap.String("filename", filename),
			zap.String("self", ss.Config.Self.Host),
			zap.Strings("placementHosts", placementHosts),
			zap.Bool("hasPlacement", hasPlacement))
		return handler.HTTPResponse{
			StatusCode: 403,
			Body:       "this node is not a placement host for the given CID",
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

	// Check if this is a replication request - if so, skip creating upload record
	if isReplication, ok := event.Upload.MetaData["isReplication"]; ok && isReplication == "true" {
		ss.logger.Debug("skipping upload record creation for replication request", zap.String("id", event.Upload.ID))
		return
	}

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

	userWallet := sql.NullString{Valid: false}
	if wallet, ok := event.Upload.MetaData["userWallet"]; ok && wallet != "" {
		userWallet = sql.NullString{String: wallet, Valid: true}
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

	// Check if this is a replication request - if so, just store the blob without processing
	if isReplication, ok := event.Upload.MetaData["isReplication"]; ok && isReplication == "true" {
		// Disk space check for replication
		if !ss.diskHasSpace() {
			ss.logger.Warn("disk is too full to accept replication", zap.String("id", event.Upload.ID))
			return
		}

		// Get filename (CID) from metadata
		filename := event.Upload.MetaData["filename"]
		if filename == "" {
			filename = event.Upload.ID
		}

		// Open the uploaded file for validation and storage
		file, err := os.Open(filePath)
		if err != nil {
			ss.logger.Error("failed to open replicated file", zap.String("id", event.Upload.ID), zap.Error(err))
			return
		}
		defer file.Close()

		// Verify CID matches content to prevent data poisoning
		computedCID, err := hashes.ComputeFileCID(file)
		if err != nil {
			ss.logger.Error("failed to compute CID for replicated file", zap.String("id", event.Upload.ID), zap.Error(err))
			return
		}
		if computedCID != filename {
			ss.logger.Error("CID verification failed for replicated file",
				zap.String("id", event.Upload.ID),
				zap.String("claimed", filename),
				zap.String("actual", computedCID))
			return
		}

		// Reset file pointer after validation
		if _, err := file.Seek(0, 0); err != nil {
			ss.logger.Error("failed to reset file pointer", zap.String("id", event.Upload.ID), zap.Error(err))
			return
		}

		// Store in bucket
		if err := ss.replicateToMyBucket(ctx, filename, file); err != nil {
			ss.logger.Error("failed to store replicated file", zap.String("id", event.Upload.ID), zap.String("filename", filename), zap.Error(err))
			return
		}

		ss.logger.Info("replication upload stored successfully",
			zap.String("id", event.Upload.ID),
			zap.String("filename", filename),
			zap.Int64("size", event.Upload.Size))
		return
	}

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
