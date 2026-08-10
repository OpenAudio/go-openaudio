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
	TiktokHandle        string `json:"tiktok_handle,omitempty"`
	Donation            string `json:"donation,omitempty"`
	// Account-state flags are always serialized: `omitempty` would drop a false
	// value, and the indexer cannot distinguish "absent" from "false" — an
	// unavailable user would silently be recorded as available.
	IsVerified         bool        `json:"is_verified"`
	IsDeactivated      bool        `json:"is_deactivated"`
	IsAvailable        bool        `json:"is_available"`
	PlaylistLibrary    interface{} `json:"playlist_library,omitempty"`
	ArtistPickTrackID  *int64      `json:"artist_pick_track_id,omitempty"`
	AllowAIAttribution bool        `json:"allow_ai_attribution,omitempty"`
	// Profile settings a live client only ever sends on Update, so the indexer's
	// production create path is not where they normally arrive. A migration
	// Create carries the account's final state, so it has to send them or they
	// are lost: 3,816 users have a USDC payout wallet, 425 a profile type and 96
	// a coin flair mint. `omitempty` is correct for all three -- an empty string
	// is the absent value, and the indexer inserts NULL for it. The source holds
	// 376 empty-string coin_flair_mint rows that must not become '' downstream.
	SplUsdcPayoutWallet string `json:"spl_usdc_payout_wallet,omitempty"`
	ProfileType         string `json:"profile_type,omitempty"`
	CoinFlairMint       string `json:"coin_flair_mint,omitempty"`
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
	TiktokHandle        *string
	Donation            *string
	IsVerified          bool
	IsDeactivated       bool
	IsAvailable         bool
	PlaylistLibrary     []byte // JSONB
	ArtistPickTrackID   *int64
	AllowAIAttribution  bool
	SplUsdcPayoutWallet *string
	// ProfileType is read as text: the source column is the profile_type_enum
	// user-defined type, which pgx cannot decode into a Go string without the
	// cast in the SELECT.
	ProfileType   *string
	CoinFlairMint *string
	CreatedAt     time.Time
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
			tiktok_handle, donation,
			is_verified, is_deactivated, is_available,
			playlist_library, artist_pick_track_id, allow_ai_attribution,
			spl_usdc_payout_wallet, profile_type::text, coin_flair_mint,
			created_at
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
				&u.TiktokHandle, &u.Donation,
				&u.IsVerified, &u.IsDeactivated, &u.IsAvailable,
				&u.PlaylistLibrary, &u.ArtistPickTrackID, &u.AllowAIAttribution,
				&u.SplUsdcPayoutWallet, &u.ProfileType, &u.CoinFlairMint,
				&u.CreatedAt,
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
				TiktokHandle:        deref(u.TiktokHandle),
				Donation:            deref(u.Donation),
				IsVerified:          u.IsVerified,
				IsDeactivated:       u.IsDeactivated,
				IsAvailable:         u.IsAvailable,
				PlaylistLibrary:     unmarshalJSONB(u.PlaylistLibrary),
				ArtistPickTrackID:   u.ArtistPickTrackID,
				AllowAIAttribution:  u.AllowAIAttribution,
				SplUsdcPayoutWallet: deref(u.SplUsdcPayoutWallet),
				ProfileType:         deref(u.ProfileType),
				CoinFlairMint:       deref(u.CoinFlairMint),
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
	IsDelete    bool
}

func (w *Writer) writeAssociatedWallets(ctx context.Context) error {
	return processBatched(ctx, w, "associated_wallets",
		`SELECT count(*) FROM associated_wallets aw
		 JOIN users u ON u.user_id = aw.user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		 WHERE aw.is_current = true`,
		`SELECT aw.user_id, aw.wallet, aw.chain, u.wallet, aw.is_delete
		FROM associated_wallets aw
		JOIN users u ON u.user_id = aw.user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		WHERE aw.is_current = true
		ORDER BY aw.user_id, aw.wallet`,
		func(rows pgx.Rows) (sourceAssociatedWallet, error) {
			var aw sourceAssociatedWallet
			err := rows.Scan(&aw.UserID, &aw.Wallet, &aw.Chain, &aw.OwnerWallet, &aw.IsDelete)
			return aw, err
		},
		func(ctx context.Context, aw sourceAssociatedWallet) error {
			metaJSON, err := json.Marshal(map[string]any{
				"wallet": aw.Wallet,
				"chain":  aw.Chain,
				// Always serialized: `omitempty` semantics would drop a false
				// value and the indexer cannot tell "absent" from "not deleted".
				"is_delete": aw.IsDelete,
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
			// An unlinked wallet carries is_delete on the Create rather than
			// arriving as a second Delete transaction: the source row is already
			// the final state, so there is no intermediate moment to replay.
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
