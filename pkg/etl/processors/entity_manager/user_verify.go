package entity_manager

import (
	"context"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

// VerifiedAddress is the wallet address authorized to sign User Verify transactions.
// Discovery-provider uses shared_config["contracts"]["verified_address"].
// TODO: Make configurable via env or metadata; for now accept any signer when empty.
var VerifiedAddress = ""

type userVerifyHandler struct{}

func (h *userVerifyHandler) EntityType() string { return EntityTypeUser }
func (h *userVerifyHandler) Action() string     { return ActionVerify }

func (h *userVerifyHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateUserVerify(ctx, params); err != nil {
		return err
	}
	return verifyUser(ctx, params)
}

func validateUserVerify(ctx context.Context, params *Params) error {
	// Stateless: entity type and action
	if params.EntityType != EntityTypeUser {
		return NewValidationError("wrong entity type %s", params.EntityType)
	}
	if params.Action != ActionVerify {
		return NewValidationError("wrong action %s", params.Action)
	}

	// Stateful: user must exist
	exists, err := userExists(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("user %d does not exist", params.UserID)
	}

	// Stateful: signer must match verified_address (when configured)
	if VerifiedAddress != "" && !strings.EqualFold(params.Signer, VerifiedAddress) {
		return NewValidationError("signer %s does not match verified address", params.Signer)
	}

	return nil
}

func verifyUser(ctx context.Context, params *Params) error {
	existing, err := getCurrentUserForVerify(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}

	isVerified := existing.isVerified
	if v, ok := params.MetadataBool("is_verified"); ok {
		isVerified = isVerified || v
	}
	twitterHandle := pickString(params.MetadataString("twitter_handle"), existing.twitterHandle)
	instagramHandle := pickString(params.MetadataString("instagram_handle"), existing.instagramHandle)
	tiktokHandle := pickString(params.MetadataString("tiktok_handle"), existing.tiktokHandle)

	verifiedWithTwitter := existing.verifiedWithTwitter
	verifiedWithInstagram := existing.verifiedWithInstagram
	verifiedWithTiktok := existing.verifiedWithTiktok
	if isVerified {
		if twitterHandle != "" {
			verifiedWithTwitter = true
		}
		if instagramHandle != "" {
			verifiedWithInstagram = true
		}
		if tiktokHandle != "" {
			verifiedWithTiktok = true
		}
	}

	_, err = params.DBTX.Exec(ctx, `
		UPDATE users SET
			is_verified = $2, twitter_handle = $3, instagram_handle = $4, tiktok_handle = $5,
			verified_with_twitter = $6, verified_with_instagram = $7, verified_with_tiktok = $8,
			updated_at = $9, txhash = $10, blocknumber = $11
		WHERE user_id = $1 AND is_current = true
	`,
		params.UserID,
		isVerified,
		twitterHandle,
		instagramHandle,
		tiktokHandle,
		verifiedWithTwitter,
		verifiedWithInstagram,
		verifiedWithTiktok,
		params.BlockTime,
		params.TxHash,
		params.BlockNumber,
	)
	return err
}

type currentUserForVerifyRow struct {
	handle                string
	handleLC              string
	wallet                string
	name                  string
	bio                   string
	location              string
	profilePicture        string
	profilePictureSizes   string
	coverPhoto            string
	coverPhotoSizes       string
	twitterHandle         string
	instagramHandle       string
	tiktokHandle          string
	verifiedWithTwitter   bool
	verifiedWithInstagram bool
	verifiedWithTiktok    bool
	isVerified            bool
	isDeactivated         bool
	isAvailable           bool
	playlistLibrary       []byte
	artistPickTrackID     *int64
	allowAIAttribution    bool
	createdAt             time.Time
}

func getCurrentUserForVerify(ctx context.Context, dbtx db.DBTX, userID int64) (*currentUserForVerifyRow, error) {
	var (
		handle, handleLC, wallet, name, bio, location                      string
		profilePicture, profilePictureSizes, coverPhoto, coverPhotoSizes   string
		twitterHandle, instagramHandle, tiktokHandle                       string
		verifiedWithTwitter, verifiedWithInstagram, verifiedWithTiktok    bool
		isVerified, isDeactivated, isAvailable                             bool
		playlistLibrary                                                    []byte
		artistPickTrackID                                                  *int64
		allowAIAttribution                                                 bool
		createdAt                                                          time.Time
	)
	err := dbtx.QueryRow(ctx, `
		SELECT COALESCE(handle,''), COALESCE(handle_lc,''), COALESCE(wallet,''),
			COALESCE(name,''), COALESCE(bio,''), COALESCE(location,''),
			COALESCE(profile_picture,''), COALESCE(profile_picture_sizes,''),
			COALESCE(cover_photo,''), COALESCE(cover_photo_sizes,''),
			COALESCE(twitter_handle,''), COALESCE(instagram_handle,''), COALESCE(tiktok_handle,''),
			COALESCE(verified_with_twitter, false), COALESCE(verified_with_instagram, false), COALESCE(verified_with_tiktok, false),
			is_verified, is_deactivated, is_available,
			playlist_library, artist_pick_track_id, allow_ai_attribution,
			created_at
		FROM users WHERE user_id = $1 AND is_current = true LIMIT 1
	`, userID).Scan(
		&handle, &handleLC, &wallet, &name, &bio, &location,
		&profilePicture, &profilePictureSizes, &coverPhoto, &coverPhotoSizes,
		&twitterHandle, &instagramHandle, &tiktokHandle,
		&verifiedWithTwitter, &verifiedWithInstagram, &verifiedWithTiktok,
		&isVerified, &isDeactivated, &isAvailable,
		&playlistLibrary, &artistPickTrackID, &allowAIAttribution,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}
	return &currentUserForVerifyRow{
		handle:                handle,
		handleLC:              handleLC,
		wallet:                wallet,
		name:                  name,
		bio:                   bio,
		location:              location,
		profilePicture:        profilePicture,
		profilePictureSizes:  profilePictureSizes,
		coverPhoto:            coverPhoto,
		coverPhotoSizes:       coverPhotoSizes,
		twitterHandle:         twitterHandle,
		instagramHandle:        instagramHandle,
		tiktokHandle:          tiktokHandle,
		verifiedWithTwitter:   verifiedWithTwitter,
		verifiedWithInstagram: verifiedWithInstagram,
		verifiedWithTiktok:     verifiedWithTiktok,
		isVerified:            isVerified,
		isDeactivated:         isDeactivated,
		isAvailable:           isAvailable,
		playlistLibrary:       playlistLibrary,
		artistPickTrackID:     artistPickTrackID,
		allowAIAttribution:    allowAIAttribution,
		createdAt:             createdAt,
	}, nil
}

// UserVerify returns the User Verify handler.
func UserVerify() Handler { return &userVerifyHandler{} }
