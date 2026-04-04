package entity_manager

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

// --- Notification Create ---

type notificationCreateHandler struct{}

func (h *notificationCreateHandler) EntityType() string { return EntityTypeNotification }
func (h *notificationCreateHandler) Action() string     { return ActionCreate }

func (h *notificationCreateHandler) Handle(ctx context.Context, params *Params) error {
	// Validate metadata is valid JSON
	if params.RawMetadata == "" {
		return NewValidationError("notification metadata is empty")
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(params.RawMetadata), &data); err != nil {
		return NewValidationError("invalid notification metadata JSON: %v", err)
	}

	groupID := fmt.Sprintf("announcement:blocknumber:%d", params.BlockNumber)

	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO notification (
			specifier, group_id, type, blocknumber, timestamp, data, user_ids
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (group_id, specifier) DO NOTHING
	`, "", groupID, "announcement", params.BlockNumber, params.BlockTime, params.RawMetadata, []int{})
	return err
}

// --- Notification View ---

type notificationViewHandler struct{}

func (h *notificationViewHandler) EntityType() string { return EntityTypeNotification }
func (h *notificationViewHandler) Action() string     { return ActionView }

func (h *notificationViewHandler) Handle(ctx context.Context, params *Params) error {
	// User must exist
	exists, err := userExists(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("user %d does not exist", params.UserID)
	}
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO notification_seen (
			user_id, seen_at, txhash, blocknumber
		) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, seen_at) DO NOTHING
	`, params.UserID, params.BlockTime, params.TxHash, params.BlockNumber)
	return err
}

// --- Notification ViewPlaylist ---

type notificationViewPlaylistHandler struct{}

func (h *notificationViewPlaylistHandler) EntityType() string { return EntityTypeNotification }
func (h *notificationViewPlaylistHandler) Action() string     { return ActionViewPlaylist }

func (h *notificationViewPlaylistHandler) Handle(ctx context.Context, params *Params) error {
	// User must exist
	exists, err := userExists(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("user %d does not exist", params.UserID)
	}
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	// Playlist must exist
	plExists, err := playlistExistsAny(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if !plExists {
		return NewValidationError("playlist %d does not exist", params.EntityID)
	}

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO playlist_seen (
			is_current, user_id, playlist_id, seen_at, txhash, blocknumber
		) VALUES (true, $1, $2, $3, $4, $5)
		ON CONFLICT (is_current, user_id, playlist_id, seen_at) DO NOTHING
	`, params.UserID, params.EntityID, params.BlockTime, params.TxHash, params.BlockNumber)
	return err
}

func playlistExistsAny(ctx context.Context, dbtx db.DBTX, playlistID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM playlists WHERE playlist_id = $1 AND is_current = true)",
		playlistID).Scan(&exists)
	return exists, err
}

// NotificationCreate returns the Notification Create handler.
func NotificationCreate() Handler { return &notificationCreateHandler{} }

// NotificationView returns the Notification View handler.
func NotificationView() Handler { return &notificationViewHandler{} }

// PlaylistSeenView returns the PlaylistSeen View handler.
func PlaylistSeenView() Handler { return &notificationViewPlaylistHandler{} }
