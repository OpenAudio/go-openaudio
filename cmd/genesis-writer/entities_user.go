package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
)

// --- Users ---

type userMetadataWrapper struct {
	CID  string       `json:"cid"`
	Data userMetadata `json:"data"`
}

type userMetadata struct {
	CreatedAt           string `json:"created_at,omitempty"`
	Name                string `json:"name,omitempty"`
	Handle              string `json:"handle,omitempty"`
	Bio                 string `json:"bio,omitempty"`
	Location            string `json:"location,omitempty"`
	Wallet              string `json:"wallet,omitempty"`
	ProfilePicture      string `json:"profile_picture,omitempty"`
	ProfilePictureSizes string `json:"profile_picture_sizes,omitempty"`
	CoverPhoto          string `json:"cover_photo,omitempty"`
	CoverPhotoSizes     string `json:"cover_photo_sizes,omitempty"`
	TwitterHandle       string `json:"twitter_handle,omitempty"`
	InstagramHandle     string `json:"instagram_handle,omitempty"`
	Website             string `json:"website,omitempty"`
	IsVerified          bool   `json:"is_verified,omitempty"`
}

type sourceUser struct {
	UserID              int64
	Wallet              *string
	Handle              *string
	Name                *string
	Bio                 *string
	Location            *string
	ProfilePicture      *string
	ProfilePictureSizes *string
	CoverPhoto          *string
	CoverPhotoSizes     *string
	TwitterHandle       *string
	InstagramHandle     *string
	Website             *string
	IsVerified          bool
	CreatedAt           time.Time
}

func (w *Writer) writeUsers(ctx context.Context) error {
	return processBatched(ctx, w, "users",
		`SELECT count(*) FROM users WHERE is_current = true AND is_deactivated = false AND is_available = true`,
		`SELECT
			user_id, wallet, handle, name, bio, location,
			profile_picture, profile_picture_sizes,
			cover_photo, cover_photo_sizes,
			twitter_handle, instagram_handle, website,
			is_verified, created_at
		FROM users
		WHERE is_current = true AND is_deactivated = false AND is_available = true
		ORDER BY user_id`,
		func(rows pgx.Rows) (sourceUser, error) {
			var u sourceUser
			err := rows.Scan(
				&u.UserID, &u.Wallet, &u.Handle, &u.Name, &u.Bio, &u.Location,
				&u.ProfilePicture, &u.ProfilePictureSizes,
				&u.CoverPhoto, &u.CoverPhotoSizes,
				&u.TwitterHandle, &u.InstagramHandle, &u.Website,
				&u.IsVerified, &u.CreatedAt,
			)
			return u, err
		},
		func(ctx context.Context, u sourceUser) error {
			meta := userMetadata{
				CreatedAt:           u.CreatedAt.Format(time.RFC3339),
				Name:                deref(u.Name),
				Handle:              deref(u.Handle),
				Bio:                 deref(u.Bio),
				Location:            deref(u.Location),
				Wallet:              deref(u.Wallet),
				ProfilePicture:      deref(u.ProfilePicture),
				ProfilePictureSizes: deref(u.ProfilePictureSizes),
				CoverPhoto:          deref(u.CoverPhoto),
				CoverPhotoSizes:     deref(u.CoverPhotoSizes),
				TwitterHandle:       deref(u.TwitterHandle),
				InstagramHandle:     deref(u.InstagramHandle),
				Website:             deref(u.Website),
				IsVerified:          u.IsVerified,
			}
			metaJSON, err := json.Marshal(userMetadataWrapper{
				CID:  "genesis-import",
				Data: meta,
			})
			if err != nil {
				return fmt.Errorf("marshal user %d metadata: %w", u.UserID, err)
			}
			signer := w.signerAddr
			if u.Wallet != nil && *u.Wallet != "" {
				signer = strings.ToLower(*u.Wallet)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     u.UserID,
				EntityType: "User",
				EntityId:   u.UserID,
				Action:     "Create",
				Metadata:   string(metaJSON),
			}, signer)
		},
	)
}

// --- Associated Wallets ---

type sourceAssociatedWallet struct {
	UserID int64
	Wallet string
	Chain  string
}

func (w *Writer) writeAssociatedWallets(ctx context.Context) error {
	return processBatched(ctx, w, "associated_wallets",
		`SELECT count(*) FROM associated_wallets WHERE is_current = true AND is_delete = false`,
		`SELECT user_id, wallet, chain
		FROM associated_wallets
		WHERE is_current = true AND is_delete = false
		ORDER BY user_id, wallet`,
		func(rows pgx.Rows) (sourceAssociatedWallet, error) {
			var aw sourceAssociatedWallet
			err := rows.Scan(&aw.UserID, &aw.Wallet, &aw.Chain)
			return aw, err
		},
		func(ctx context.Context, aw sourceAssociatedWallet) error {
			metaJSON, err := json.Marshal(map[string]string{
				"wallet": aw.Wallet,
				"chain":  aw.Chain,
			})
			if err != nil {
				return fmt.Errorf("marshal associated wallet: %w", err)
			}
			// DP uses params.signer (the wallet address) as identity for AssociatedWallet.
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     aw.UserID,
				EntityType: "AssociatedWallet",
				EntityId:   0,
				Action:     "Create",
				Metadata:   string(metaJSON),
			}, aw.Wallet)
		},
	)
}
