package server

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/storyicon/sigverify"
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
		PreUploadCreateCallback: ss.validateTusUploadBeforeCreate,
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

func (ss *MediorumServer) validateTusUploadBeforeCreate(event handler.HookEvent) (handler.HTTPResponse, handler.FileInfoChanges, error) {
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
		return handler.HTTPResponse{
			StatusCode: 401,
			Body:       "replication upload missing authentication",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	// Parse Basic auth
	if !strings.HasPrefix(authHeader, "Basic ") {
		return handler.HTTPResponse{
			StatusCode: 401,
			Body:       "invalid auth header format",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	decoded, err := base64.StdEncoding.DecodeString(authHeader[6:])
	if err != nil {
		return handler.HTTPResponse{
			StatusCode: 401,
			Body:       "failed to decode auth header",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	parts := strings.Split(string(decoded), ":")
	if len(parts) != 2 {
		return handler.HTTPResponse{
			StatusCode: 401,
			Body:       "invalid auth format",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	user, pass := parts[0], parts[1]

	// Check timestamp age
	ts, err := strconv.ParseInt(user, 0, 64)
	if err != nil {
		return handler.HTTPResponse{
			StatusCode: 401,
			Body:       "invalid timestamp in auth",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}
	if age := time.Since(time.UnixMilli(ts)); age > time.Hour {
		return handler.HTTPResponse{
			StatusCode: 401,
			Body:       fmt.Sprintf("authentication expired (age: %v)", age),
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	// Recover wallet from signature
	if len(pass) < 2 || !strings.HasPrefix(pass, "0x") {
		return handler.HTTPResponse{
			StatusCode: 401,
			Body:       "invalid signature format",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}
	sig, err := hex.DecodeString(pass[2:]) // remove "0x"
	if err != nil {
		return handler.HTTPResponse{
			StatusCode: 401,
			Body:       "invalid signature hex",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	hash := crypto.Keccak256Hash([]byte(user))
	wallet, err := sigverify.EcRecoverEx(hash.Bytes(), sig)
	if err != nil {
		return handler.HTTPResponse{
			StatusCode: 401,
			Body:       "failed to recover wallet",
		}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
	}

	// Verify wallet is in peer list
	for _, peer := range ss.Config.Peers {
		if strings.EqualFold(peer.Wallet, wallet.Hex()) {
			ss.logger.Debug("authenticated TUS replication upload",
				zap.String("wallet", wallet.Hex()),
				zap.String("uploadID", event.Upload.ID))
			return handler.HTTPResponse{}, handler.FileInfoChanges{}, nil
		}
	}

	return handler.HTTPResponse{
		StatusCode: 403,
		Body:       fmt.Sprintf("wallet not in peer list: %s", wallet.Hex()),
	}, handler.FileInfoChanges{}, handler.ErrUploadRejectedByServer
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

		// Validate CID matches file content (security check)
		if err := cidutil.ValidateCID(filename, file); err != nil {
			ss.logger.Error("replication CID validation failed",
				zap.String("id", event.Upload.ID),
				zap.String("filename", filename),
				zap.Error(err))
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
	err := ss.crud.DB.First(&upload, "id = ?", event.Upload.ID).Error
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
