package entity_manager

import (
	"context"
)

// --- Comment Notification Mute ---

type commentMuteHandler struct{}

func (h *commentMuteHandler) EntityType() string { return EntityTypeComment }
func (h *commentMuteHandler) Action() string     { return ActionMute }

func (h *commentMuteHandler) Handle(ctx context.Context, params *Params) error {
	return upsertCommentNotificationSetting(ctx, params, true)
}

// --- Comment Notification Unmute ---

type commentUnmuteHandler struct{}

func (h *commentUnmuteHandler) EntityType() string { return EntityTypeComment }
func (h *commentUnmuteHandler) Action() string     { return ActionUnmute }

func (h *commentUnmuteHandler) Handle(ctx context.Context, params *Params) error {
	return upsertCommentNotificationSetting(ctx, params, false)
}

func upsertCommentNotificationSetting(ctx context.Context, params *Params, isMuted bool) error {
	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO comment_notification_settings (
			user_id, entity_id, entity_type, is_muted,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (user_id, entity_id, entity_type)
		DO UPDATE SET is_muted = $4, updated_at = $5
	`, params.UserID, params.EntityID, params.EntityType, isMuted, params.BlockTime)
	return err
}

// CommentMute returns the Comment Mute handler.
func CommentMute() Handler { return &commentMuteHandler{} }

// CommentUnmute returns the Comment Unmute handler.
func CommentUnmute() Handler { return &commentUnmuteHandler{} }
