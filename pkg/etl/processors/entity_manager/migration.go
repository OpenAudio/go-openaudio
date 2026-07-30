package entity_manager

import (
	"context"
	"strings"
)

// Genesis migration handlers.
//
// genesis-writer replays historical Discovery Provider state onto a new chain as
// ManageEntityLegacyMigration transactions. Those rows are not new submissions:
// they carry their original legacy ids, they predate the content limits and
// uniqueness constraints that now guard new entities, and their authority is the
// genesis migration key (verified at the chain layer) rather than the entity
// owner's wallet.
//
// Rather than teach every production handler about migration, the migration
// dispatcher starts from the production handler set and replaces only the
// handlers whose *validation policy* differs. The actual work — the INSERT, route
// generation, stems, aggregates — is the shared production code, so there is one
// implementation of "how an entity is written" and no risk of the two paths
// drifting apart.
//
// What a migration handler still enforces:
//   - shape: the entity type and action the handler is registered for
//   - idempotency: refuse to overwrite an entity that already exists
//
// What it deliberately does not enforce, and why:
//   - legacy ID offsets — migrated ids are below them by definition
//   - content limits (bio/name/handle/description) and reserved handles —
//     legacy rows predate these rules; dropping the rows would lose real data
//   - wallet/handle uniqueness — legacy state is the source of truth and may
//     contain duplicates that predate the constraint
//   - wallet ownership proofs (`wallet_signature`, dashboard wallet ecrecover) —
//     those signatures were produced interactively when the wallet was first
//     linked and are not retained by the source tables, so they cannot be
//     replayed; the genesis migration authority attests to them instead
//
// Note that signer *authority* (ValidateSigner) is still enforced wherever the
// source provides enough information to satisfy it: genesis-writer sends each
// entity's owning wallet as the signer, so those checks pass normally rather
// than being skipped.

// RegisterMigrationOverrides replaces the handlers whose validation policy
// differs for replayed historical state. Call it on a dispatcher that already
// has the production handlers registered: every entity type not listed here
// keeps its production behavior unchanged.
func RegisterMigrationOverrides(d *Dispatcher) {
	d.Register(migratedUserCreate())
	d.Register(migratedTrackCreate())
	d.Register(migratedPlaylistCreate())
	d.Register(migratedAssociatedWalletCreate())
	d.Register(migratedDashboardWalletCreate())

	// Social rows reuse their production validator and insert verbatim; the only
	// difference is that is_delete comes from the source row rather than being
	// hardcoded false, so a soft-deleted follow/save/repost/subscription is
	// replayed as one transaction instead of a create/delete pair.
	d.Register(migratedSocial(EntityTypeAny, ActionFollow, validateFollow, insertFollow))
	d.Register(migratedSocial(EntityTypeAny, ActionSave, validateSave, insertSave))
	d.Register(migratedSocial(EntityTypeAny, ActionRepost, validateRepost, insertRepost))
	d.Register(migratedSocial(EntityTypeAny, ActionSubscribe, validateSubscribe, insertSubscription))
}

// migratedSocialHandler adapts a production social handler so the row's
// is_delete state is taken from metadata. Both the validation and the write are
// the production functions.
type migratedSocialHandler struct {
	entityType string
	action     string
	validate   func(context.Context, *Params) error
	insert     func(context.Context, *Params, bool) error
}

func (h migratedSocialHandler) EntityType() string { return h.entityType }
func (h migratedSocialHandler) Action() string     { return h.action }

func (h migratedSocialHandler) Handle(ctx context.Context, params *Params) error {
	if err := h.validate(ctx, params); err != nil {
		return err
	}
	return h.insert(ctx, params, params.MetadataBoolOr("is_delete", false))
}

func migratedSocial(
	entityType, action string,
	validate func(context.Context, *Params) error,
	insert func(context.Context, *Params, bool) error,
) Handler {
	return migratedSocialHandler{entityType: entityType, action: action, validate: validate, insert: insert}
}

// migratedTrackCreateHandler replays a historical track, including one that was
// soft-deleted or made unavailable: the source row is the truth, and dropping it
// would leave the migrated catalog short of the dump.
type migratedTrackCreateHandler struct{}

func (h *migratedTrackCreateHandler) EntityType() string { return EntityTypeTrack }
func (h *migratedTrackCreateHandler) Action() string     { return ActionCreate }

func (h *migratedTrackCreateHandler) Handle(ctx context.Context, params *Params) error {
	if params.EntityType != EntityTypeTrack {
		return NewValidationError("wrong entity type %s", params.EntityType)
	}
	if params.Metadata == nil {
		return NewValidationError("metadata is required for track creation")
	}
	ownerID, ok := params.MetadataInt64("owner_id")
	if !ok {
		return NewValidationError("owner_id is required in metadata for track creation")
	}
	if ownerID != params.UserID {
		return NewValidationError("metadata owner_id must match transaction user id")
	}
	// Genre still normalizes so the stored form is canonical, but an over-long
	// legacy genre is not a reason to drop the track.
	if genre := params.MetadataString("genre"); genre != "" {
		params.Metadata["genre"] = NormalizeGenre(genre)
	}
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	exists, err := trackExists(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if exists {
		return NewValidationError("track %d already exists", params.EntityID)
	}

	return insertTrackAndRouteWithState(ctx, params, params.MetadataBoolOr("is_delete", false))
}

func migratedTrackCreate() Handler { return &migratedTrackCreateHandler{} }

// migratedPlaylistCreateHandler replays a historical playlist, including
// soft-deleted rows, for the same reason as tracks.
type migratedPlaylistCreateHandler struct{}

func (h *migratedPlaylistCreateHandler) EntityType() string { return EntityTypePlaylist }
func (h *migratedPlaylistCreateHandler) Action() string     { return ActionCreate }

func (h *migratedPlaylistCreateHandler) Handle(ctx context.Context, params *Params) error {
	if params.EntityType != EntityTypePlaylist {
		return NewValidationError("wrong entity type %s", params.EntityType)
	}
	if params.Metadata == nil {
		return NewValidationError("metadata is required for playlist creation")
	}
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	exists, err := playlistExists(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if exists {
		return NewValidationError("playlist %d already exists", params.EntityID)
	}

	return insertPlaylistAndRouteWithState(ctx, params, params.MetadataBoolOr("is_delete", false))
}

func migratedPlaylistCreate() Handler { return &migratedPlaylistCreateHandler{} }

// migratedUserCreateHandler replays a historical user row.
type migratedUserCreateHandler struct{}

func (h *migratedUserCreateHandler) EntityType() string { return EntityTypeUser }
func (h *migratedUserCreateHandler) Action() string     { return ActionCreate }

func (h *migratedUserCreateHandler) Handle(ctx context.Context, params *Params) error {
	if params.EntityType != EntityTypeUser {
		return NewValidationError("wrong entity type %s", params.EntityType)
	}

	// Idempotency: the source can contain more than one is_current row for a
	// user id, which would otherwise replay as duplicate creates.
	exists, err := userExists(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}
	if exists {
		return NewValidationError("user %d already exists", params.UserID)
	}

	// Account state comes from the source row. Absent flags fall back to the
	// same defaults a new account would get.
	return insertUserWithState(ctx, params, userState{
		IsVerified:    params.MetadataBoolOr("is_verified", false),
		IsDeactivated: params.MetadataBoolOr("is_deactivated", false),
		IsAvailable:   params.MetadataBoolOr("is_available", true),
	})
}

func migratedUserCreate() Handler { return &migratedUserCreateHandler{} }

// migratedAssociatedWalletCreateHandler replays a historical associated wallet.
//
// The only production check it drops is the `wallet_signature` ownership proof:
// that signature was produced by the wallet holder when they originally linked
// the wallet and is not retained in the source table, so it cannot be replayed.
// Ownership is instead attested by the genesis migration authority. Signer
// authority is still enforced — genesis-writer sends the user's own wallet as the
// signer, so ValidateSigner passes normally.
type migratedAssociatedWalletCreateHandler struct{}

func (h *migratedAssociatedWalletCreateHandler) EntityType() string {
	return EntityTypeAssociatedWallet
}
func (h *migratedAssociatedWalletCreateHandler) Action() string { return ActionCreate }

func (h *migratedAssociatedWalletCreateHandler) Handle(ctx context.Context, params *Params) error {
	chain := params.MetadataString("chain")
	wallet := canonicalizeWallet(params.MetadataString("wallet"), chain)

	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	if err := validateAssociatedWalletShape(wallet, chain); err != nil {
		return err
	}

	return insertAssociatedWallet(ctx, params, wallet, chain)
}

func migratedAssociatedWalletCreate() Handler { return &migratedAssociatedWalletCreateHandler{} }

// migratedDashboardWalletCreateHandler replays a historical dashboard wallet
// association. As with associated wallets, the only dropped check is the
// ecrecover signature proof, which the source table does not retain.
type migratedDashboardWalletCreateHandler struct{}

func (h *migratedDashboardWalletCreateHandler) EntityType() string {
	return EntityTypeDashboardWalletUser
}
func (h *migratedDashboardWalletCreateHandler) Action() string { return ActionCreate }

func (h *migratedDashboardWalletCreateHandler) Handle(ctx context.Context, params *Params) error {
	wallet := strings.ToLower(params.MetadataString("wallet"))
	if wallet == "" {
		return NewValidationError("dashboard wallet address is required")
	}

	if _, err := validateDashboardWalletAssignment(ctx, params, wallet); err != nil {
		return err
	}

	return insertDashboardWalletUser(ctx, params, wallet)
}

func migratedDashboardWalletCreate() Handler { return &migratedDashboardWalletCreateHandler{} }
