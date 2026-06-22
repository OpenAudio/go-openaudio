package entity_manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
	"go.uber.org/zap"
)

// Entity type constants.
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
	EntityTypeTrackCollaborator         = "TrackCollaborator"
	EntityTypePlayCount                 = "PlayCount"
)

// Action constants.
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
	ActionReconcile   = "Reconcile"
)

// ID offsets.
const (
	PlaylistIDOffset = 400_000
	TrackIDOffset    = 2_000_000
	UserIDOffset     = 3_000_000
	CommentIDOffset  = 4_000_000
)

// Character limit constants.
const (
	CharacterLimitUserBio     = 256
	CharacterLimitUserName    = 32
	CharacterLimitHandle      = 30
	CharacterLimitDescription = 2500
	CharacterLimitCommentBody = 400
)

// MaxTrackCollaborators bounds how many collaborators a single track may invite,
// so a malformed or hostile metadata blob can't enqueue an unbounded number of
// rows. Excess entries beyond the cap are ignored.
const MaxTrackCollaborators = 50

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
	BlockHash   string
	TxHash      string
	DBTX        db.DBTX
	Logger      *zap.Logger
}

// Queries returns a sqlc Queries handle from the underlying DBTX.
func (p *Params) Queries() *db.Queries {
	return db.New(p.DBTX)
}

// NewParams creates Params from a ManageEntityLegacy proto and block context.
func NewParams(tx *corev1.ManageEntityLegacy, blockNumber int64, blockTime time.Time, blockHash, txHash string, dbtx db.DBTX, logger *zap.Logger) *Params {
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
		BlockHash:   blockHash,
		TxHash:      txHash,
		DBTX:        dbtx,
		Logger:      logger,
	}

	if tx.GetMetadata() != "" {
		var meta map[string]any
		if err := json.Unmarshal([]byte(tx.GetMetadata()), &meta); err == nil {
			// Unwrap nested "data" envelope: {"cid":"...", "data": {actual fields}}
			if data, ok := meta["data"].(map[string]any); ok {
				p.Metadata = data
			} else {
				p.Metadata = meta
			}
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

// MetadataInt64 returns an int64 from parsed metadata (supports number and string).
func (p *Params) MetadataInt64(key string) (int64, bool) {
	if p.Metadata == nil {
		return 0, false
	}
	v, ok := p.Metadata[key]
	if !ok {
		return 0, false
	}
	switch val := v.(type) {
	case float64:
		return int64(val), true
	case int:
		return int64(val), true
	case int64:
		return val, true
	}
	return 0, false
}

// MetadataFloat64 returns a float64 from parsed metadata (supports number and
// integer JSON values). Returns ok=false when the key is absent or not numeric.
func (p *Params) MetadataFloat64(key string) (float64, bool) {
	if p.Metadata == nil {
		return 0, false
	}
	v, ok := p.Metadata[key]
	if !ok {
		return 0, false
	}
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	}
	return 0, false
}

// MetadataBool returns a bool from parsed metadata.
func (p *Params) MetadataBool(key string) (bool, bool) {
	if p.Metadata == nil {
		return false, false
	}
	v, ok := p.Metadata[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// MetadataBoolOr returns the bool value or default if the key is absent.
func (p *Params) MetadataBoolOr(key string, def bool) bool {
	if v, ok := p.MetadataBool(key); ok {
		return v
	}
	return def
}

// MetadataJSON returns the raw value for a JSONB column (map, slice, etc.).
// Caller should json.Marshal for DB insertion.
func (p *Params) MetadataJSON(key string) (any, bool) {
	if p.Metadata == nil {
		return nil, false
	}
	v, ok := p.Metadata[key]
	return v, ok
}

// Handler processes a specific (entity_type, action) pair.
type Handler interface {
	EntityType() string
	Action() string
	Handle(ctx context.Context, params *Params) error
}

// PostHook fires after a successful Handler.Handle for a registered
// (entity_type, action) pair. It receives the same Params the handler did,
// so it has access to the proto (Params.TX), the DB tx (Params.DBTX), the
// parsed metadata, and block context.
//
// Hooks run only when the parent handler returned nil — a ValidationError
// or other handler failure short-circuits the dispatch before any hook
// fires. Errors returned from a hook itself are logged but do NOT fail the
// parent dispatch; this matches the semantics of apps' Postgres triggers
// (which swallow errors via `EXCEPTION WHEN others THEN raise warning`)
// and prevents a buggy consumer-side hook from halting the indexer.
//
// Multiple hooks may be registered for the same key; they run in
// registration order.
type PostHook func(ctx context.Context, params *Params) error

// Dispatcher routes ManageEntity transactions to registered handlers.
type Dispatcher struct {
	handlers  map[string]Handler
	postHooks map[string][]PostHook
	logger    *zap.Logger
}

// NewDispatcher creates a Dispatcher with no registered handlers.
func NewDispatcher(logger *zap.Logger) *Dispatcher {
	return &Dispatcher{
		handlers:  make(map[string]Handler),
		postHooks: make(map[string][]PostHook),
		logger:    logger,
	}
}

func handlerKey(entityType, action string) string {
	return entityType + ":" + action
}

// Register adds a handler for a specific (entity_type, action) pair.
func (d *Dispatcher) Register(h Handler) {
	d.handlers[handlerKey(h.EntityType(), h.Action())] = h
}

// RegisterPostHook attaches fn to fire after every successful Handle for
// (entityType, action). See PostHook for error and ordering semantics.
//
// Wildcard entityType (EntityTypeAny) is supported and follows the same
// fallback rule as Register: a tx whose (type, action) has no exact hook
// match will fire any hooks registered against (EntityTypeAny, action).
func (d *Dispatcher) RegisterPostHook(entityType, action string, fn PostHook) {
	key := handlerKey(entityType, action)
	d.postHooks[key] = append(d.postHooks[key], fn)
}

// EntityTypeAny is a wildcard entity type for handlers that match any entity type
// for a given action (e.g., social features: Follow matches entity_type "User",
// Save matches "Track" or "Playlist").
const EntityTypeAny = "*"

// Dispatch routes a ManageEntity transaction to the appropriate handler.
// Returns nil if no handler is registered (unhandled entity/action pairs are silently skipped).
// Returns a ValidationError if the handler rejects the transaction.
// Returns a non-ValidationError for unexpected failures.
//
// After a successful handler invocation, any post-hooks registered for the
// same (entity_type, action) — or for (EntityTypeAny, action) as a
// fallback — run in registration order. Hook errors are logged but do not
// propagate.
func (d *Dispatcher) Dispatch(ctx context.Context, params *Params) error {
	key := handlerKey(params.EntityType, params.Action)
	h, ok := d.handlers[key]
	hookKey := key
	if !ok {
		// Fall back to wildcard entity type match
		hookKey = handlerKey(EntityTypeAny, params.Action)
		h, ok = d.handlers[hookKey]
		if !ok {
			return nil
		}
	}
	if err := h.Handle(ctx, params); err != nil {
		return err
	}
	d.runPostHooks(ctx, key, hookKey, params)
	return nil
}

// runPostHooks fires hooks for the dispatched key and (separately) the
// wildcard-action key, isolating hook failures so one bad hook doesn't
// prevent siblings from running and doesn't fail the parent dispatch.
func (d *Dispatcher) runPostHooks(ctx context.Context, exactKey, fallbackKey string, params *Params) {
	hooks := d.postHooks[exactKey]
	if exactKey != fallbackKey {
		// Also run hooks attached to the wildcard-fallback key so a hook
		// registered as (EntityTypeAny, action) fires for every entity
		// type of that action.
		hooks = append(hooks, d.postHooks[fallbackKey]...)
	}
	for _, hook := range hooks {
		if err := hook(ctx, params); err != nil && d.logger != nil {
			d.logger.Warn("post-hook returned error",
				zap.String("entity_type", params.EntityType),
				zap.String("action", params.Action),
				zap.Int64("entity_id", params.EntityID),
				zap.Error(err))
		}
	}
}

// HasHandler returns true if a handler is registered for the given entity_type and action.
func (d *Dispatcher) HasHandler(entityType, action string) bool {
	if _, ok := d.handlers[handlerKey(entityType, action)]; ok {
		return true
	}
	_, ok := d.handlers[handlerKey(EntityTypeAny, action)]
	return ok
}

// HandlerCount returns the number of registered handlers.
func (d *Dispatcher) HandlerCount() int {
	return len(d.handlers)
}
