package entity_manager

import (
	"context"
)

// Track Mute/Unmute update comment notification settings for tracks.

type trackMuteHandler struct{}

func (h *trackMuteHandler) EntityType() string { return EntityTypeTrack }
func (h *trackMuteHandler) Action() string     { return ActionMute }

func (h *trackMuteHandler) Handle(ctx context.Context, params *Params) error {
	return upsertCommentNotificationSetting(ctx, params, true)
}

type trackUnmuteHandler struct{}

func (h *trackUnmuteHandler) EntityType() string { return EntityTypeTrack }
func (h *trackUnmuteHandler) Action() string     { return ActionUnmute }

func (h *trackUnmuteHandler) Handle(ctx context.Context, params *Params) error {
	return upsertCommentNotificationSetting(ctx, params, false)
}

// TrackMute returns the Track Mute handler (comment notification settings).
func TrackMute() Handler { return &trackMuteHandler{} }

// TrackUnmute returns the Track Unmute handler (comment notification settings).
func TrackUnmute() Handler { return &trackUnmuteHandler{} }
