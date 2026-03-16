package entity_manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/etl/db"
	"go.uber.org/zap"
)

// Entity type constants matching discovery-provider EntityType enum.
const (
	EntityTypeUser                      = "User"
	EntityTypeTrack                     = "Track"
	EntityTypePlaylist                  = "Playlist"
	EntityTypeDashboardWalletUser       = "DashboardWalletUser"
	EntityTypeUserWallet                = "UserWallet"
	EntityTypeFollow                    = "Follow"
	EntityTypeSave                      = "Save"
	EntityTypeRepost                    = "Repost"
	EntityTypeSubscription              = "Subscription"
	EntityTypeNotificationSeen          = "NotificationSeen"
	EntityTypeNotification              = "Notification"
	EntityTypePlaylistSeen              = "PlaylistSeen"
	EntityTypeDeveloperApp              = "DeveloperApp"
	EntityTypeGrant                     = "Grant"
	EntityTypeAssociatedWallet          = "AssociatedWallet"
	EntityTypeUserEvent                 = "UserEvent"
	EntityTypeStem                      = "Stem"
	EntityTypeRemix                     = "Remix"
	EntityTypeTrackRoute                = "TrackRoute"
	EntityTypePlaylistRoute             = "PlaylistRoute"
	EntityTypeTip                       = "Tip"
	EntityTypeComment                   = "Comment"
	EntityTypeCommentReaction           = "CommentReaction"
	EntityTypeCommentReport             = "CommentReport"
	EntityTypeCommentThread             = "CommentThread"
	EntityTypeCommentMention            = "CommentMention"
	EntityTypeMutedUser                 = "MutedUser"
	EntityTypeCommentNotificationSetting = "CommentNotificationSetting"
	EntityTypeEncryptedEmail            = "EncryptedEmail"
	EntityTypeEmailAccess               = "EmailAccess"
	EntityTypeEvent                     = "Event"
	EntityTypeShare                     = "Share"
)

// Action constants matching discovery-provider Action enum.
const (
	ActionCreate      = "Create"
	ActionUpdate      = "Update"
	ActionDelete      = "Delete"
	ActionFollow      = "Follow"
	ActionUnfollow    = "Unfollow"
	ActionSave        = "Save"
	ActionUnsave      = "Unsave"
	ActionRepost      = "Repost"
	ActionUnrepost    = "Unrepost"
	ActionVerify      = "Verify"
	ActionSubscribe   = "Subscribe"
	ActionUnsubscribe = "Unsubscribe"
	ActionView        = "View"
	ActionViewPlaylist = "ViewPlaylist"
	ActionApprove     = "Approve"
	ActionReject      = "Reject"
	ActionDownload    = "Download"
	ActionReact       = "React"
	ActionUnreact     = "Unreact"
	ActionPin         = "Pin"
	ActionUnpin       = "Unpin"
	ActionMute        = "Mute"
	ActionUnmute      = "Unmute"
	ActionAddEmail    = "AddEmail"
	ActionReport      = "Report"
	ActionShare       = "Share"
)

// ID offsets matching discovery-provider constants.
const (
	UserIDOffset     = 1_000_000
	TrackIDOffset    = 1_000_000
	PlaylistIDOffset = 1_000_000
)

// Character limit constants matching discovery-provider.
const (
	CharacterLimitUserBio     = 256
	CharacterLimitUserName    = 32
	CharacterLimitHandle      = 30
	CharacterLimitDescription = 1000
)

// ValidationError indicates a transaction should be skipped (not a fatal indexing error).
type ValidationError struct {
	msg string
}

func (e *ValidationError) Error() string {
	return e.msg
}

func NewValidationError(format string, args ...any) *ValidationError {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// IsValidationError returns true if the error is a ValidationError.
func IsValidationError(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

// Params holds all context for processing a single ManageEntity transaction.
type Params struct {
	TX          *corev1.ManageEntityLegacy
	UserID      int64
	EntityID    int64
	EntityType  string
	Action      string
	Signer      string
	Metadata    map[string]any
	RawMetadata string
	BlockNumber int64
	BlockTime   time.Time
	TxHash      string
	DBTX        db.DBTX
	Logger      *zap.Logger
}

// Queries returns a sqlc Queries handle from the underlying DBTX.
func (p *Params) Queries() *db.Queries {
	return db.New(p.DBTX)
}

// NewParams creates Params from a ManageEntityLegacy proto and block context.
func NewParams(tx *corev1.ManageEntityLegacy, blockNumber int64, blockTime time.Time, txHash string, dbtx db.DBTX, logger *zap.Logger) *Params {
	p := &Params{
		TX:          tx,
		UserID:      tx.GetUserId(),
		EntityID:    tx.GetEntityId(),
		EntityType:  tx.GetEntityType(),
		Action:      tx.GetAction(),
		Signer:      tx.GetSigner(),
		RawMetadata: tx.GetMetadata(),
		BlockNumber: blockNumber,
		BlockTime:   blockTime,
		TxHash:      txHash,
		DBTX:        dbtx,
		Logger:      logger,
	}

	if tx.GetMetadata() != "" {
		var meta map[string]any
		if err := json.Unmarshal([]byte(tx.GetMetadata()), &meta); err == nil {
			p.Metadata = meta
		}
	}

	return p
}

// MetadataString returns a string field from parsed metadata, or empty string.
func (p *Params) MetadataString(key string) string {
	if p.Metadata == nil {
		return ""
	}
	v, ok := p.Metadata[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// Handler processes a specific (entity_type, action) pair.
type Handler interface {
	EntityType() string
	Action() string
	Handle(ctx context.Context, params *Params) error
}

// Dispatcher routes ManageEntity transactions to registered handlers.
type Dispatcher struct {
	handlers map[string]Handler
	logger   *zap.Logger
}

// NewDispatcher creates a Dispatcher with no registered handlers.
func NewDispatcher(logger *zap.Logger) *Dispatcher {
	return &Dispatcher{
		handlers: make(map[string]Handler),
		logger:   logger,
	}
}

func handlerKey(entityType, action string) string {
	return entityType + ":" + action
}

// Register adds a handler for a specific (entity_type, action) pair.
func (d *Dispatcher) Register(h Handler) {
	d.handlers[handlerKey(h.EntityType(), h.Action())] = h
}

// Dispatch routes a ManageEntity transaction to the appropriate handler.
// Returns nil if no handler is registered (unhandled entity/action pairs are silently skipped).
// Returns a ValidationError if the handler rejects the transaction.
// Returns a non-ValidationError for unexpected failures.
func (d *Dispatcher) Dispatch(ctx context.Context, params *Params) error {
	key := handlerKey(params.EntityType, params.Action)
	h, ok := d.handlers[key]
	if !ok {
		return nil
	}
	return h.Handle(ctx, params)
}

// HasHandler returns true if a handler is registered for the given entity_type and action.
func (d *Dispatcher) HasHandler(entityType, action string) bool {
	_, ok := d.handlers[handlerKey(entityType, action)]
	return ok
}

// HandlerCount returns the number of registered handlers.
func (d *Dispatcher) HandlerCount() int {
	return len(d.handlers)
}
