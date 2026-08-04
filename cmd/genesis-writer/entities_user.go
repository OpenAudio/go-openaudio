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
	// Account-state flags are always serialized: `omitempty` would drop a false
	// value, and the indexer cannot distinguish "absent" from "false" — an
	// unavailable user would silently be recorded as available.
	IsVerified    bool `json:"is_verified"`
	IsDeactivated bool `json:"is_deactivated"`
	IsAvailable   bool `json:"is_available"`
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
	IsDeactivated       bool
	IsAvailable         bool
	CreatedAt           time.Time
}

// writeUsers migrates every current user row, including deactivated and
// unavailable accounts. Their state travels in the metadata so the indexer can
// reproduce it: filtering them out here would orphan the tracks, playlists and
// social rows they own (the indexer rejects those with "user does not exist")
// and would make a parity check against the source unable to distinguish
// intentional omissions from real data loss.
func (w *Writer) writeUsers(ctx context.Context) error {
	return processBatched(ctx, w, "users",
		`SELECT count(*) FROM users WHERE is_current = true`,
		`SELECT
			user_id, wallet, handle, name, bio, location,
			profile_picture, profile_picture_sizes,
			cover_photo, cover_photo_sizes,
			twitter_handle, instagram_handle, website,
			is_verified, is_deactivated, is_available, created_at
		FROM users
		WHERE is_current = true
		ORDER BY user_id`,
		func(rows pgx.Rows) (sourceUser, error) {
			var u sourceUser
			err := rows.Scan(
				&u.UserID, &u.Wallet, &u.Handle, &u.Name, &u.Bio, &u.Location,
				&u.ProfilePicture, &u.ProfilePictureSizes,
				&u.CoverPhoto, &u.CoverPhotoSizes,
				&u.TwitterHandle, &u.InstagramHandle, &u.Website,
				&u.IsVerified, &u.IsDeactivated, &u.IsAvailable, &u.CreatedAt,
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
				IsDeactivated:       u.IsDeactivated,
				IsAvailable:         u.IsAvailable,
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
	// OwnerWallet is the owning user's own wallet, used as the transaction
	// signer. Joined here rather than looked up per row.
	OwnerWallet *string
}

func (w *Writer) writeAssociatedWallets(ctx context.Context) error {
	return processBatched(ctx, w, "associated_wallets",
		`SELECT count(*) FROM associated_wallets aw
		 JOIN users u ON u.user_id = aw.user_id AND u.is_current = true
		 WHERE aw.is_current = true AND aw.is_delete = false`,
		`SELECT aw.user_id, aw.wallet, aw.chain, u.wallet
		FROM associated_wallets aw
		JOIN users u ON u.user_id = aw.user_id AND u.is_current = true
		WHERE aw.is_current = true AND aw.is_delete = false
		ORDER BY aw.user_id, aw.wallet`,
		func(rows pgx.Rows) (sourceAssociatedWallet, error) {
			var aw sourceAssociatedWallet
			err := rows.Scan(&aw.UserID, &aw.Wallet, &aw.Chain, &aw.OwnerWallet)
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
			// Sign as the owning user, not as the wallet being linked. The
			// indexer takes the associated wallet itself from the metadata above
			// and uses the signer only to authorize the change against the user,
			// so sending the wallet here would fail that authority check for
			// every row. The wallet's own ownership proof is not retained by the
			// source table and cannot be replayed.
			ownerWallet := w.signerAddr
			if aw.OwnerWallet != nil && *aw.OwnerWallet != "" {
				ownerWallet = strings.ToLower(*aw.OwnerWallet)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     aw.UserID,
				EntityType: "AssociatedWallet",
				EntityId:   0,
				Action:     "Create",
				Metadata:   string(metaJSON),
			}, ownerWallet)
		},
	)
}
