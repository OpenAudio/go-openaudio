package entity_manager

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
	"github.com/jackc/pgx/v5"
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
	// Social links set via profile edit (unverified). The verified_with_* flags
	// and verification-sourced handles stay owned by the UserVerify handler.
	twitterHandle := mergeNullStr(params, "twitter_handle", existing.twitterHandle)
	instagramHandle := mergeNullStr(params, "instagram_handle", existing.instagramHandle)
	tiktokHandle := mergeNullStr(params, "tiktok_handle", existing.tiktokHandle)
	website := mergeNullStr(params, "website", existing.website)
	donation := mergeNullStr(params, "donation", existing.donation)

	splUsdcPayoutWallet := mergeNullStr(params, "spl_usdc_payout_wallet", existing.splUsdcPayoutWallet)
	coinFlairMint := mergeNullStr(params, "coin_flair_mint", existing.coinFlairMint)
	// profile_type is a Postgres enum; an unknown label must fail loudly rather
	// than be stored, so only values the type declares are accepted.
	profileType := existing.profileType
	if v := params.MetadataString("profile_type"); v != "" && isKnownProfileType(v) {
		profileType = &v
	}

	artistPickTrackID := existing.artistPickTrackID
	if trackID, ok := params.MetadataInt64("artist_pick_track_id"); ok {
		artistPickTrackID = &trackID
	}

	allowAIAttribution := existing.allowAIAttribution
	if v, ok := params.MetadataBool("allow_ai_attribution"); ok {
		allowAIAttribution = v
	}

	isDeactivated := existing.isDeactivated
	if v, ok := params.MetadataBool("is_deactivated"); ok {
		isDeactivated = v
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
			twitter_handle = $11, instagram_handle = $12, tiktok_handle = $13, website = $14, donation = $15,
			playlist_library = $16, artist_pick_track_id = $17, allow_ai_attribution = $18,
			is_deactivated = $19, updated_at = $20, txhash = $21, blocknumber = $22,
			spl_usdc_payout_wallet = $23, coin_flair_mint = $24, profile_type = $25::profile_type_enum
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
		strPtrVal(twitterHandle),
		strPtrVal(instagramHandle),
		strPtrVal(tiktokHandle),
		strPtrVal(website),
		strPtrVal(donation),
		playlistLibrary,
		artistPickTrackID,
		allowAIAttribution,
		isDeactivated,
		params.BlockTime,
		params.TxHash,
		params.BlockNumber,
		strPtrVal(splUsdcPayoutWallet),
		strPtrVal(coinFlairMint),
		strPtrVal(profileType),
	)
	return err
}

// profileTypeEnumValues mirrors the profile_type_enum labels in migration 0036.
var profileTypeEnumValues = map[string]struct{}{"label": {}}

func isKnownProfileType(v string) bool {
	_, ok := profileTypeEnumValues[v]
	return ok
}

// mergeNullStr returns the metadata value if present and non-empty;
// otherwise it preserves the existing value.
//
// The chain convention is "empty string = no change". Treating "" as "clear
// the field" caused real data corruption against production data: User
// Update txs with `"handle":""` (meaning the client didn't want to change
// handle) were wiping `users.handle` to NULL while leaving `handle_lc`
// populated, producing an inconsistent row. Matches the prod indexer's
// behavior.
//
// Callers that genuinely need to clear a field on chain-supplied null
// should check for that explicitly upstream of this helper.
func mergeNullStr(p *Params, key string, existing *string) *string {
	if _, ok := p.Metadata[key]; !ok {
		return existing
	}
	s := p.MetadataString(key)
	if s == "" {
		return existing
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
	twitterHandle       *string
	instagramHandle     *string
	tiktokHandle        *string
	website             *string
	donation            *string
	playlistLibrary     []byte
	splUsdcPayoutWallet *string
	coinFlairMint       *string
	profileType         *string
	artistPickTrackID   *int64
	allowAIAttribution  bool
	isVerified          bool
	isDeactivated       bool
	isAvailable         bool
	createdAt           time.Time
}

func getCurrentUser(ctx context.Context, dbtx db.DBTX, userID int64) (*currentUserRow, error) {
	var (
		handle, handleLC, wallet, name, bio, location                    sql.NullString
		profilePicture, profilePictureSizes, coverPhoto, coverPhotoSizes sql.NullString
		twitterHandle, instagramHandle, tiktokHandle, website, donation  sql.NullString
		playlistLibrary                                                  []byte
		splUsdcPayoutWallet, coinFlairMint, profileType                  sql.NullString
		artistPickTrackID                                                *int64
		allowAIAttribution, isVerified, isDeactivated, isAvailable       bool
		createdAt                                                        time.Time
	)
	err := dbtx.QueryRow(ctx, `
		SELECT handle, handle_lc, wallet,
			name, bio, location,
			profile_picture, profile_picture_sizes,
			cover_photo, cover_photo_sizes,
			twitter_handle, instagram_handle, tiktok_handle, website, donation,
			playlist_library, spl_usdc_payout_wallet, coin_flair_mint, profile_type,
			artist_pick_track_id, allow_ai_attribution,
			is_verified, is_deactivated, is_available, created_at
		FROM users WHERE user_id = $1 AND is_current = true LIMIT 1
	`, userID).Scan(
		&handle, &handleLC, &wallet, &name, &bio, &location,
		&profilePicture, &profilePictureSizes, &coverPhoto, &coverPhotoSizes,
		&twitterHandle, &instagramHandle, &tiktokHandle, &website, &donation,
		&playlistLibrary, &splUsdcPayoutWallet, &coinFlairMint, &profileType,
		&artistPickTrackID, &allowAIAttribution,
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
		twitterHandle:       nullStrPtr(twitterHandle),
		instagramHandle:     nullStrPtr(instagramHandle),
		tiktokHandle:        nullStrPtr(tiktokHandle),
		website:             nullStrPtr(website),
		donation:            nullStrPtr(donation),
		playlistLibrary:     playlistLibrary,
		splUsdcPayoutWallet: nullStrPtr(splUsdcPayoutWallet),
		coinFlairMint:       nullStrPtr(coinFlairMint),
		profileType:         nullStrPtr(profileType),
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
	if errors.Is(err, pgx.ErrNoRows) {
		// User has no current row (deleted between validation and now, or
		// never existed under is_current=true). Treat as empty handle —
		// callers compare against the new handle, so empty here means
		// "different, run the uniqueness check".
		return "", nil
	}
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
