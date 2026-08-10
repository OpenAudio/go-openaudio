package entity_manager

import (
	"context"
	"encoding/json"
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
//   - rules about the current moment ("this contest has already ended", "this
//     entity already has one running") — a replayed row records something that
//     ran and finished long ago, so the source already holds the answer and the
//     rule only gets to disagree with it
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
	d.Register(migratedCommentCreate())
	d.Register(migratedEventCreate())
	d.Register(migratedDeveloperAppCreate())
	d.Register(migratedGrantCreate())
	d.Register(migratedAssociatedWalletCreate())
	d.Register(migratedDashboardWalletCreate())

	// Social rows reuse their production validator and insert verbatim; the only
	// difference is that is_delete comes from the source row rather than being
	// hardcoded false, so a soft-deleted follow/save/repost/subscription is
	// replayed as one transaction instead of a create/delete pair.
	d.Register(migratedSocial(EntityTypeAny, ActionFollow, validateFollow, insertMigratedFollow))
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

	return insertTrackAndRouteWithState(ctx, params, trackState{
		IsDelete: params.MetadataBoolOr("is_delete", false),
		Route:    migratedTrackRoute(params),
	})
}

// migratedTrackRoute reads the route the writer carried from the source, or
// returns nil so the slug is generated as it would be for a new track.
//
// Legacy slugs are not reproducible from the title: the rules changed over the
// catalog's life. On a production clone 618,066 of 1,955,877 tracks regenerate
// to a slug different from the one they serve today -- 330,856 because the old
// scheme appended the track id, the rest over punctuation handling and
// collision numbering. Untitled tracks are the clearest case: the source holds
// a random slug like "k2rX2M3" that nothing can derive.
func migratedTrackRoute(params *Params) *trackRoute {
	slug := params.MetadataString("route_slug")
	if slug == "" {
		return nil
	}
	titleSlug := params.MetadataString("route_title_slug")
	if titleSlug == "" {
		titleSlug = slug
	}
	collisionID, _ := params.MetadataInt64("route_collision_id")
	return &trackRoute{Slug: slug, TitleSlug: titleSlug, CollisionID: int(collisionID)}
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

	return insertPlaylistAndRouteWithState(ctx, params, playlistState{
		IsDelete:      params.MetadataBoolOr("is_delete", false),
		Route:         migratedPlaylistRoute(params),
		RemovedTracks: migratedPlaylistRemovedTracks(params),
	})
}

// migratedPlaylistRemovedTracks reads the removal history the writer carried
// from the source's playlist_tracks rows with is_removed = true, under the
// `removed_tracks` metadata key:
//
//	[{"track_id": 1, "created_at": "<rfc3339>", "updated_at": "<rfc3339>"}, ...]
//
// created_at is when the track joined the playlist, updated_at when it left.
// Absent for a production create, and for the vast majority of migrated
// playlists — 3,917 of 312,552 have any removal history at all.
func migratedPlaylistRemovedTracks(params *Params) []removedPlaylistTrack {
	raw, ok := params.MetadataJSON("removed_tracks")
	if !ok {
		return nil
	}
	entries, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]removedPlaylistTrack, 0, len(entries))
	for _, entry := range entries {
		obj, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		trackID, ok := pickPlaylistTrackID(obj)
		if !ok {
			continue
		}
		removed := removedPlaylistTrack{TrackID: trackID}
		if ts, ok := parseReleaseDate(stringField(obj, "updated_at")); ok {
			removed.UpdatedAt = ts.Time
		}
		if ts, ok := parseReleaseDate(stringField(obj, "created_at")); ok {
			removed.CreatedAt = ts.Time
		}
		out = append(out, removed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// stringField reads a string out of a decoded JSON object, or "" if absent or
// of another type.
func stringField(obj map[string]any, key string) string {
	s, _ := obj[key].(string)
	return s
}

// migratedPlaylistRoute reads the route the writer carried from the source, or
// returns nil so the slug is generated as it would be for a new playlist. See
// migratedTrackRoute -- playlist slugs drifted the same way, and further: 60.7%
// of migrated playlists regenerate to a slug they do not serve today, against
// 31.6% of tracks.
func migratedPlaylistRoute(params *Params) *playlistRoute {
	slug := params.MetadataString("route_slug")
	if slug == "" {
		return nil
	}
	titleSlug := params.MetadataString("route_title_slug")
	if titleSlug == "" {
		titleSlug = slug
	}
	collisionID, _ := params.MetadataInt64("route_collision_id")
	return &playlistRoute{Slug: slug, TitleSlug: titleSlug, CollisionID: int(collisionID)}
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
	// playlist_library, artist_pick_track_id and allow_ai_attribution are read
	// here and not in the production create handler on purpose: a live client
	// only ever sends them on Update. Measured on a production clone, among the
	// 292,111 users never modified after creation, artist_pick_track_id and
	// allow_ai_attribution appear exactly zero times. A migration Create carries
	// an account's final state, so it must accept them or they are lost --
	// 622,480 users have a playlist library, 13,935 an artist pick, 1,484 the AI
	// attribution flag.
	state := userState{
		IsVerified:         params.MetadataBoolOr("is_verified", false),
		IsDeactivated:      params.MetadataBoolOr("is_deactivated", false),
		IsAvailable:        params.MetadataBoolOr("is_available", true),
		AllowAIAttribution: params.MetadataBoolOr("allow_ai_attribution", false),
	}
	if v, ok := params.MetadataJSON("playlist_library"); ok && v != nil {
		if jb, err := json.Marshal(v); err == nil {
			state.PlaylistLibrary = jb
		}
	}
	if trackID, ok := params.MetadataInt64("artist_pick_track_id"); ok {
		state.ArtistPickTrackID = &trackID
	}
	if v := params.MetadataString("profile_type"); isKnownProfileType(v) {
		state.ProfileType = &v
	}
	// spl_usdc_payout_wallet and coin_flair_mint are read on a production create
	// too (CreateUserSchema carries the payout wallet), but this handler builds
	// its own userState and so has to read them itself or they are dropped for
	// every migrated row: 3,816 users have a payout wallet and 96 a coin flair
	// mint. An empty string inserts as NULL, which matters -- the source holds
	// 376 empty-string coin_flair_mint rows that are not a set value.
	state.SplUsdcPayoutWallet = nullStrPtrFromMeta(params, "spl_usdc_payout_wallet")
	state.CoinFlairMint = nullStrPtrFromMeta(params, "coin_flair_mint")
	return insertUserWithState(ctx, params, state)
}

func migratedUserCreate() Handler { return &migratedUserCreateHandler{} }

// The three handlers below all exist for the same reason: their entity carries
// a soft-deleted or unapproved state that the production create path hardcodes
// to "active", because a client cannot create an already deleted row. A
// migration can, and must -- the source row is the truth. Carrying the state on
// the Create keeps it to one transaction per row instead of a Create followed
// by a Delete, which matters when the alternative is several hundred thousand
// extra transactions to index.

type migratedCommentCreateHandler struct{}

func (h *migratedCommentCreateHandler) EntityType() string { return EntityTypeComment }
func (h *migratedCommentCreateHandler) Action() string     { return ActionCreate }

func (h *migratedCommentCreateHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateCommentWrite(ctx, params, true); err != nil {
		return err
	}
	return insertCommentWithState(ctx, params, params.MetadataBoolOr("is_delete", false))
}

func migratedCommentCreate() Handler { return &migratedCommentCreateHandler{} }

// migratedEventCreateHandler replays a historical event, including a remix
// contest that has already ended.
//
// The production create asks whether the contest may open *now*: its end_date
// must be in the future, and the entity must not already have a contest still
// running. Both questions are answered against block time, which for a
// migration tx is the source row's created_at (see migrationBlockTime in the
// indexer) -- so on replay "now" is a moment years in the past that the source
// has already lived through and recorded the outcome of.
//
// The end_date rule survives that only by luck: nothing in the source ends
// before it was created, so as long as every event carries a parseable
// created_at the rule never fires. It is one metadata change away from firing
// on everything. Without that timestamp, block time falls back to the block's
// own -- migration wall-clock -- and 109 of the 112 live events on the
// 2026-08-07 snapshot have an end_date in the past. A rule that quietly drops
// 97% of a table when an unrelated field goes missing, taking the subscriptions
// that point at those events with it, does not belong on the replay path.
//
// The uniqueness rule costs a row as things stand. Track 1174134089 holds two
// contests created two minutes apart with the same end date (events 925703801
// and 1458921100); replaying the second finds the first still running and
// rejects it. Both are in the dump, so both must land: the source is the record
// of what happened, not a submission to be re-adjudicated.
//
// Everything in validateEventCreateShape still runs, so a migrated event is
// still signed by its user, still lands once, and still hangs off a user and
// track that exist.
type migratedEventCreateHandler struct{}

func (h *migratedEventCreateHandler) EntityType() string { return EntityTypeEvent }
func (h *migratedEventCreateHandler) Action() string     { return ActionCreate }

func (h *migratedEventCreateHandler) Handle(ctx context.Context, params *Params) error {
	if params.EntityType != EntityTypeEvent {
		return NewValidationError("wrong entity type %s", params.EntityType)
	}
	if _, err := validateEventCreateShape(ctx, params); err != nil {
		return err
	}
	return insertEvent(ctx, params)
}

func migratedEventCreate() Handler { return &migratedEventCreateHandler{} }

type migratedDeveloperAppCreateHandler struct{}

func (h *migratedDeveloperAppCreateHandler) EntityType() string {
	return EntityTypeDeveloperApp
}
func (h *migratedDeveloperAppCreateHandler) Action() string { return ActionCreate }

func (h *migratedDeveloperAppCreateHandler) Handle(ctx context.Context, params *Params) error {
	return insertDeveloperAppWithState(ctx, params, params.MetadataBoolOr("is_delete", false))
}

func migratedDeveloperAppCreate() Handler { return &migratedDeveloperAppCreateHandler{} }

type migratedGrantCreateHandler struct{}

func (h *migratedGrantCreateHandler) EntityType() string { return EntityTypeGrant }
func (h *migratedGrantCreateHandler) Action() string     { return ActionCreate }

func (h *migratedGrantCreateHandler) Handle(ctx context.Context, params *Params) error {
	state := grantState{IsRevoked: params.MetadataBoolOr("is_revoked", false)}
	// An absent is_approved keeps the production derivation, which is right for
	// the 79 grants the source leaves NULL. A present value wins, including
	// false: rejecting a grant records is_approved = false alongside
	// is_revoked = true, and no follow-up Grant/Approve or Grant/Reject
	// transaction can reproduce that pair -- Approve forces is_revoked to false,
	// and both require the grantee's own wallet as the signer.
	if v, ok := params.MetadataBool("is_approved"); ok {
		state.IsApproved = &v
	}
	return insertGrantWithState(ctx, params, state)
}

func migratedGrantCreate() Handler { return &migratedGrantCreateHandler{} }

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

	return insertAssociatedWalletWithState(ctx, params, wallet, chain, params.MetadataBoolOr("is_delete", false))
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

	return insertDashboardWalletUserWithState(ctx, params, wallet, params.MetadataBoolOr("is_delete", false))
}

func migratedDashboardWalletCreate() Handler { return &migratedDashboardWalletCreateHandler{} }
