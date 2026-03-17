package entity_manager

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

type userUpdateHandler struct{}

func (h *userUpdateHandler) EntityType() string { return EntityTypeUser }
func (h *userUpdateHandler) Action() string     { return ActionUpdate }

func (h *userUpdateHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateUserUpdate(ctx, params); err != nil {
		return err
	}
	return updateUser(ctx, params)
}

func validateUserUpdate(ctx context.Context, params *Params) error {
	// Stateless: entity type and action
	if params.EntityType != EntityTypeUser {
		return NewValidationError("wrong entity type %s", params.EntityType)
	}
	if params.Action != ActionUpdate {
		return NewValidationError("wrong action %s", params.Action)
	}

	// Stateless: bio length
	if bio := params.MetadataString("bio"); bio != "" {
		if err := ValidateBio(bio); err != nil {
			return err
		}
	}

	// Stateless: name length
	if name := params.MetadataString("name"); name != "" {
		if err := ValidateUserName(name); err != nil {
			return err
		}
	}

	// Stateless: handle format (if changing)
	if handle := params.MetadataString("handle"); handle != "" {
		if err := ValidateHandle(handle); err != nil {
			return err
		}
	}

	// Stateful: signer must match user wallet
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	// Stateful: user must exist
	exists, err := userExists(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("user %d does not exist", params.UserID)
	}

	// Stateful: if handle changed, check uniqueness
	if handle := params.MetadataString("handle"); handle != "" {
		existingHandle, err := getUserHandle(ctx, params.DBTX, params.UserID)
		if err != nil {
			return err
		}
		newHandleLC := strings.ToLower(handle)
		// Only check if actually changing
		if existingHandle != newHandleLC {
			handleTaken, err := handleExists(ctx, params.DBTX, newHandleLC)
			if err != nil {
				return err
			}
			if handleTaken {
				return NewValidationError("handle %q already exists", handle)
			}
		}
	}

	// Stateful: if artist_pick_track_id set, track must exist and be owned by user
	if trackID, ok := params.MetadataInt64("artist_pick_track_id"); ok && trackID != 0 {
		owned, err := trackExistsAndOwnedBy(ctx, params.DBTX, trackID, params.UserID)
		if err != nil {
			return err
		}
		if !owned {
			return NewValidationError("track %d does not exist or is not owned by user %d", trackID, params.UserID)
		}
	}

	return nil
}

func updateUser(ctx context.Context, params *Params) error {
	// Fetch existing user row for merge
	existing, err := getCurrentUser(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}

	// Mark current row as not current
	if err := markNotCurrent(ctx, params.DBTX, "users", "user_id", params.UserID); err != nil {
		return err
	}

	// Merge metadata into existing
	handle := pickString(params.MetadataString("handle"), existing.handle)
	handleLC := strings.ToLower(handle)
	if handle == "" {
		handleLC = existing.handleLC
	}
	name := pickString(params.MetadataString("name"), existing.name)
	bio := pickString(params.MetadataString("bio"), existing.bio)
	location := pickString(params.MetadataString("location"), existing.location)
	profilePicture := pickString(params.MetadataString("profile_picture"), existing.profilePicture)
	profilePictureSizes := pickString(params.MetadataString("profile_picture_sizes"), existing.profilePictureSizes)
	coverPhoto := pickString(params.MetadataString("cover_photo"), existing.coverPhoto)
	coverPhotoSizes := pickString(params.MetadataString("cover_photo_sizes"), existing.coverPhotoSizes)

	artistPickTrackID := existing.artistPickTrackID
	if trackID, ok := params.MetadataInt64("artist_pick_track_id"); ok {
		artistPickTrackID = &trackID
	} else if params.MetadataString("artist_pick_track_id") == "" {
		// Explicit null in metadata would clear it - for now keep existing if not in metadata
	}

	allowAIAttribution := existing.allowAIAttribution
	if v, ok := params.MetadataBool("allow_ai_attribution"); ok {
		allowAIAttribution = v
	}

	playlistLibrary := existing.playlistLibrary
	if v, ok := params.MetadataJSON("playlist_library"); ok && v != nil {
		jb, err := json.Marshal(v)
		if err == nil {
			playlistLibrary = jb
		}
	}

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO users (
			user_id, handle, handle_lc, wallet, name, bio, location,
			profile_picture, profile_picture_sizes, cover_photo, cover_photo_sizes,
			playlist_library, artist_pick_track_id, allow_ai_attribution,
			is_current, is_verified, is_deactivated, is_available, is_storage_v2,
			created_at, updated_at, txhash, blockhash, blocknumber
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11,
			$12, $13, $14,
			true, $15, $16, $17, true,
			$18, $19, $20, '', $21
		)
	`,
		params.UserID,
		handle,
		handleLC,
		existing.wallet,
		name,
		bio,
		location,
		profilePicture,
		profilePictureSizes,
		coverPhoto,
		coverPhotoSizes,
		playlistLibrary,
		artistPickTrackID,
		allowAIAttribution,
		existing.isVerified,
		existing.isDeactivated,
		existing.isAvailable,
		existing.createdAt,
		params.BlockTime,
		params.TxHash,
		params.BlockNumber,
	)
	return err
}

func pickString(newVal, existing string) string {
	if newVal != "" {
		return newVal
	}
	return existing
}

type currentUserRow struct {
	handle              string
	handleLC            string
	wallet              string
	name                string
	bio                 string
	location            string
	profilePicture      string
	profilePictureSizes string
	coverPhoto          string
	coverPhotoSizes     string
	playlistLibrary     []byte
	artistPickTrackID   *int64
	allowAIAttribution  bool
	isVerified          bool
	isDeactivated       bool
	isAvailable         bool
	createdAt           time.Time
}

func getCurrentUser(ctx context.Context, dbtx db.DBTX, userID int64) (*currentUserRow, error) {
	var (
		handle, handleLC, wallet, name, bio, location                string
		profilePicture, profilePictureSizes, coverPhoto, coverPhotoSizes string
		playlistLibrary                                               []byte
		artistPickTrackID                                             *int64
		allowAIAttribution, isVerified, isDeactivated, isAvailable    bool
		createdAt                                                     time.Time
	)
	err := dbtx.QueryRow(ctx, `
		SELECT COALESCE(handle,''), COALESCE(handle_lc,''), COALESCE(wallet,''),
			COALESCE(name,''), COALESCE(bio,''), COALESCE(location,''),
			COALESCE(profile_picture,''), COALESCE(profile_picture_sizes,''),
			COALESCE(cover_photo,''), COALESCE(cover_photo_sizes,''),
			playlist_library, artist_pick_track_id, allow_ai_attribution,
			is_verified, is_deactivated, is_available, created_at
		FROM users WHERE user_id = $1 AND is_current = true LIMIT 1
	`, userID).Scan(
		&handle, &handleLC, &wallet, &name, &bio, &location,
		&profilePicture, &profilePictureSizes, &coverPhoto, &coverPhotoSizes,
		&playlistLibrary, &artistPickTrackID, &allowAIAttribution,
		&isVerified, &isDeactivated, &isAvailable, &createdAt,
	)
	if err != nil {
		return nil, err
	}
	return &currentUserRow{
		handle:              handle,
		handleLC:            handleLC,
		wallet:              wallet,
		name:                name,
		bio:                 bio,
		location:            location,
		profilePicture:      profilePicture,
		profilePictureSizes: profilePictureSizes,
		coverPhoto:          coverPhoto,
		coverPhotoSizes:     coverPhotoSizes,
		playlistLibrary:     playlistLibrary,
		artistPickTrackID:   artistPickTrackID,
		allowAIAttribution:  allowAIAttribution,
		isVerified:         isVerified,
		isDeactivated:      isDeactivated,
		isAvailable:        isAvailable,
		createdAt:           createdAt,
	}, nil
}

func getUserHandle(ctx context.Context, dbtx db.DBTX, userID int64) (string, error) {
	var handleLC string
	err := dbtx.QueryRow(ctx, "SELECT handle_lc FROM users WHERE user_id = $1 AND is_current = true LIMIT 1", userID).Scan(&handleLC)
	return handleLC, err
}

func trackExistsAndOwnedBy(ctx context.Context, dbtx db.DBTX, trackID, ownerID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM tracks WHERE track_id = $1 AND owner_id = $2 AND is_current = true AND is_delete = false
		)
	`, trackID, ownerID).Scan(&exists)
	return exists, err
}

// UserUpdate returns the User Update handler.
func UserUpdate() Handler { return &userUpdateHandler{} }
