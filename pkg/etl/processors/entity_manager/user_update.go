package entity_manager

import (
	"context"
	"database/sql"
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
	existing, err := getCurrentUser(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}

	handle := mergeNullStr(params, "handle", existing.handle)
	handleLC := existing.handleLC
	if handle != nil {
		lc := strings.ToLower(*handle)
		handleLC = &lc
	}
	name := mergeNullStr(params, "name", existing.name)
	bio := mergeNullStr(params, "bio", existing.bio)
	location := mergeNullStr(params, "location", existing.location)
	profilePicture := mergeNullStr(params, "profile_picture", existing.profilePicture)
	profilePictureSizes := mergeNullStr(params, "profile_picture_sizes", existing.profilePictureSizes)
	coverPhoto := mergeNullStr(params, "cover_photo", existing.coverPhoto)
	coverPhotoSizes := mergeNullStr(params, "cover_photo_sizes", existing.coverPhotoSizes)

	artistPickTrackID := existing.artistPickTrackID
	if trackID, ok := params.MetadataInt64("artist_pick_track_id"); ok {
		artistPickTrackID = &trackID
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
		UPDATE users SET
			handle = $2, handle_lc = $3, name = $4, bio = $5, location = $6,
			profile_picture = $7, profile_picture_sizes = $8, cover_photo = $9, cover_photo_sizes = $10,
			playlist_library = $11, artist_pick_track_id = $12, allow_ai_attribution = $13,
			updated_at = $14, txhash = $15, blocknumber = $16
		WHERE user_id = $1 AND is_current = true
	`,
		params.UserID,
		strPtrVal(handle),
		strPtrVal(handleLC),
		strPtrVal(name),
		strPtrVal(bio),
		strPtrVal(location),
		strPtrVal(profilePicture),
		strPtrVal(profilePictureSizes),
		strPtrVal(coverPhoto),
		strPtrVal(coverPhotoSizes),
		playlistLibrary,
		artistPickTrackID,
		allowAIAttribution,
		params.BlockTime,
		params.TxHash,
		params.BlockNumber,
	)
	return err
}

// mergeNullStr returns the metadata value if present, otherwise the existing value.
// If metadata provides an empty string, it clears the field (returns nil).
func mergeNullStr(p *Params, key string, existing *string) *string {
	if _, ok := p.Metadata[key]; !ok {
		return existing
	}
	s := p.MetadataString(key)
	if s == "" {
		return nil
	}
	return &s
}

type currentUserRow struct {
	handle              *string
	handleLC            *string
	wallet              *string
	name                *string
	bio                 *string
	location            *string
	profilePicture      *string
	profilePictureSizes *string
	coverPhoto          *string
	coverPhotoSizes     *string
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
		handle, handleLC, wallet, name, bio, location                      sql.NullString
		profilePicture, profilePictureSizes, coverPhoto, coverPhotoSizes   sql.NullString
		playlistLibrary                                                    []byte
		artistPickTrackID                                                  *int64
		allowAIAttribution, isVerified, isDeactivated, isAvailable         bool
		createdAt                                                          time.Time
	)
	err := dbtx.QueryRow(ctx, `
		SELECT handle, handle_lc, wallet,
			name, bio, location,
			profile_picture, profile_picture_sizes,
			cover_photo, cover_photo_sizes,
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
		handle:              nullStrPtr(handle),
		handleLC:            nullStrPtr(handleLC),
		wallet:              nullStrPtr(wallet),
		name:                nullStrPtr(name),
		bio:                 nullStrPtr(bio),
		location:            nullStrPtr(location),
		profilePicture:      nullStrPtr(profilePicture),
		profilePictureSizes: nullStrPtr(profilePictureSizes),
		coverPhoto:          nullStrPtr(coverPhoto),
		coverPhotoSizes:     nullStrPtr(coverPhotoSizes),
		playlistLibrary:     playlistLibrary,
		artistPickTrackID:   artistPickTrackID,
		allowAIAttribution:  allowAIAttribution,
		isVerified:          isVerified,
		isDeactivated:       isDeactivated,
		isAvailable:         isAvailable,
		createdAt:           createdAt,
	}, nil
}

func getUserHandle(ctx context.Context, dbtx db.DBTX, userID int64) (string, error) {
	var handleLC sql.NullString
	err := dbtx.QueryRow(ctx, "SELECT handle_lc FROM users WHERE user_id = $1 AND is_current = true LIMIT 1", userID).Scan(&handleLC)
	if handleLC.Valid {
		return handleLC.String, err
	}
	return "", err
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
