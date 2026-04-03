package entity_manager

import (
	"context"
	"database/sql"
	"strings"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

// VerifiedAddress is the wallet address authorized to sign User Verify transactions.
var VerifiedAddress = "0xbeef8E42e8B5964fDD2b7ca8efA0d9aef38AA996"

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
	twitterHandle := mergeNullStr(params, "twitter_handle", existing.twitterHandle)
	instagramHandle := mergeNullStr(params, "instagram_handle", existing.instagramHandle)
	tiktokHandle := mergeNullStr(params, "tiktok_handle", existing.tiktokHandle)

	verifiedWithTwitter := existing.verifiedWithTwitter
	verifiedWithInstagram := existing.verifiedWithInstagram
	verifiedWithTiktok := existing.verifiedWithTiktok
	if isVerified {
		if twitterHandle != nil {
			verifiedWithTwitter = true
		}
		if instagramHandle != nil {
			verifiedWithInstagram = true
		}
		if tiktokHandle != nil {
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
		strPtrVal(twitterHandle),
		strPtrVal(instagramHandle),
		strPtrVal(tiktokHandle),
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
	twitterHandle         *string
	instagramHandle       *string
	tiktokHandle          *string
	verifiedWithTwitter   bool
	verifiedWithInstagram bool
	verifiedWithTiktok    bool
	isVerified            bool
}

func getCurrentUserForVerify(ctx context.Context, dbtx db.DBTX, userID int64) (*currentUserForVerifyRow, error) {
	var (
		twitterHandle, instagramHandle, tiktokHandle                   sql.NullString
		verifiedWithTwitter, verifiedWithInstagram, verifiedWithTiktok sql.NullBool
		isVerified                                                     bool
	)
	err := dbtx.QueryRow(ctx, `
		SELECT twitter_handle, instagram_handle, tiktok_handle,
			verified_with_twitter, verified_with_instagram, verified_with_tiktok,
			is_verified
		FROM users WHERE user_id = $1 AND is_current = true LIMIT 1
	`, userID).Scan(
		&twitterHandle, &instagramHandle, &tiktokHandle,
		&verifiedWithTwitter, &verifiedWithInstagram, &verifiedWithTiktok,
		&isVerified,
	)
	if err != nil {
		return nil, err
	}
	return &currentUserForVerifyRow{
		twitterHandle:         nullStrPtr(twitterHandle),
		instagramHandle:       nullStrPtr(instagramHandle),
		tiktokHandle:          nullStrPtr(tiktokHandle),
		verifiedWithTwitter:   verifiedWithTwitter.Valid && verifiedWithTwitter.Bool,
		verifiedWithInstagram: verifiedWithInstagram.Valid && verifiedWithInstagram.Bool,
		verifiedWithTiktok:    verifiedWithTiktok.Valid && verifiedWithTiktok.Bool,
		isVerified:            isVerified,
	}, nil
}

// UserVerify returns the User Verify handler.
func UserVerify() Handler { return &userVerifyHandler{} }
