package entity_manager

import (
	"context"
	"strings"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
)

type userCreateHandler struct{}

func (h *userCreateHandler) EntityType() string { return EntityTypeUser }
func (h *userCreateHandler) Action() string     { return ActionCreate }

func (h *userCreateHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateUserCreate(ctx, params); err != nil {
		return err
	}
	return insertUser(ctx, params)
}

func validateUserCreate(ctx context.Context, params *Params) error {
	// Stateless: entity type
	if params.EntityType != EntityTypeUser {
		return NewValidationError("wrong entity type %s", params.EntityType)
	}

	// Stateless: user_id offset
	if params.UserID < UserIDOffset {
		return NewValidationError("user id %d below offset %d", params.UserID, UserIDOffset)
	}

	// Stateless: bio length (check from metadata if present)
	if bio := params.MetadataString("bio"); bio != "" {
		if err := ValidateBio(bio); err != nil {
			return err
		}
	}

	// Stateless: handle format (if present in metadata)
	handle := params.MetadataString("handle")
	if handle != "" {
		if err := ValidateHandle(handle); err != nil {
			return err
		}
	}

	// Stateless: name length
	if name := params.MetadataString("name"); name != "" {
		if err := ValidateUserName(name); err != nil {
			return err
		}
	}

	// Stateful: user must not already exist
	exists, err := userExists(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}
	if exists {
		return NewValidationError("user %d already exists", params.UserID)
	}

	// Stateful: wallet must not already be in use
	walletUsed, err := walletExists(ctx, params.DBTX, params.Signer)
	if err != nil {
		return err
	}
	if walletUsed {
		return NewValidationError("wallet %s already in use", params.Signer)
	}

	// Stateful: signer must not be a developer app
	isDeveloperApp, err := developerAppExists(ctx, params.DBTX, params.Signer)
	if err != nil {
		return err
	}
	if isDeveloperApp {
		return NewValidationError("developer app %s cannot create user", params.Signer)
	}

	// Stateful: handle uniqueness
	if handle != "" {
		handleTaken, err := handleExists(ctx, params.DBTX, strings.ToLower(handle))
		if err != nil {
			return err
		}
		if handleTaken {
			return NewValidationError("handle %q already exists", handle)
		}
	}

	return nil
}

// insertUser writes a newly created user. New accounts are always unverified,
// active and available: those flags are not self-assignable (verification goes
// through the Verify action, deactivation through Update).
func insertUser(ctx context.Context, params *Params) error {
	state := userState{IsAvailable: true}
	// profile_type is a Postgres enum; only values the type declares are
	// accepted, mirroring the Update handler.
	if v := params.MetadataString("profile_type"); isKnownProfileType(v) {
		state.ProfileType = &v
	}
	return insertUserWithState(ctx, params, state)
}

// userState carries the account-state flags for an inserted user. Only trusted
// callers supply a non-default value — see the genesis migration handlers, which
// replay these flags from the source row.
type userState struct {
	IsVerified    bool
	IsDeactivated bool
	IsAvailable   bool

	// Profile fields the live protocol only ever sends on Update -- a client
	// sets them by editing a profile, never at signup. Production leaves them
	// zero here; only the migration replay populates them, because it collapses
	// an account's whole history into a single Create and would otherwise drop
	// them. Zero values are indistinguishable from "not set" and insert as NULL
	// or false, matching a fresh account.
	PlaylistLibrary    []byte
	ArtistPickTrackID  *int64
	AllowAIAttribution bool

	// Unlike the fields above, profile_type is also accepted on a live Create:
	// the API's create schema carries it, so an account (e.g. a label) can
	// declare it at signup. Both callers validate against the enum first.
	ProfileType *string
}

// ON CONFLICT DO NOTHING covers both unique indexes on users: users_pkey
// (user_id, txhash) for a re-delivered tx, and users_current_uniq_idx
// (user_id) WHERE is_current for a create against a user that already exists.
//
// Both callers do check first — validateUserCreate and
// migratedUserCreateHandler each call userExists — so this is unreachable from
// a single writer, where the check and the insert share a transaction. It is
// reachable from a second one: userExists then INSERT is check-then-act, and
// that is not atomic across transactions, so a concurrent writer can pass its
// own check before this insert commits. That is the most plausible account of
// the five duplicate current rows 0035 cleans up, three of which pair a
// bare-hex txhash with a 0x-prefixed one.
//
// Letting the loser raise 23505 instead would be worse than a no-op: a hard
// error here rolls back the savepoint and drops the tx entirely (its audit row
// included), which is silent apart from a log line. DO NOTHING is the correct
// outcome anyway — the row it would have written already exists.
func insertUserWithState(ctx context.Context, params *Params, state userState) error {
	handle := nullString(params.MetadataString("handle"))
	var handleLC any
	if h := params.MetadataString("handle"); h != "" {
		handleLC = strings.ToLower(h)
	}

	// nil rather than an empty slice so the column inserts as NULL, not as an
	// empty jsonb value.
	var playlistLibrary any
	if len(state.PlaylistLibrary) > 0 {
		playlistLibrary = state.PlaylistLibrary
	}

	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO users (
			user_id, handle, handle_lc, wallet, name, bio, location,
			profile_picture, profile_picture_sizes, cover_photo, cover_photo_sizes,
			twitter_handle, instagram_handle, tiktok_handle, website, donation,
			is_current, is_verified, is_deactivated, is_available, is_storage_v2,
			playlist_library, artist_pick_track_id, allow_ai_attribution, profile_type,
			created_at, updated_at, txhash, blockhash, blocknumber
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11,
			$12, $13, $14, $15, $16,
			true, $25, $26, $27, true,
			$21, $22, $23, $24,
			$17, $17, $18, $19, $20
		)
		ON CONFLICT DO NOTHING
	`,
		params.UserID,
		handle,
		handleLC,
		strings.ToLower(params.Signer),
		nullString(params.MetadataString("name")),
		nullString(params.MetadataString("bio")),
		nullString(params.MetadataString("location")),
		nullString(params.MetadataString("profile_picture")),
		nullString(params.MetadataString("profile_picture_sizes")),
		nullString(params.MetadataString("cover_photo")),
		nullString(params.MetadataString("cover_photo_sizes")),
		nullString(params.MetadataString("twitter_handle")),
		nullString(params.MetadataString("instagram_handle")),
		nullString(params.MetadataString("tiktok_handle")),
		nullString(params.MetadataString("website")),
		nullString(params.MetadataString("donation")),
		params.BlockTime,
		params.TxHash,
		params.BlockHash,
		params.BlockNumber,
		// The three state flags stay the trailing arguments; tests assert on that
		// position deliberately, so new columns are bound ahead of them.
		playlistLibrary,
		state.ArtistPickTrackID,
		state.AllowAIAttribution,
		strPtrVal(state.ProfileType),
		state.IsVerified,
		state.IsDeactivated,
		state.IsAvailable,
	)
	return err
}

func userExists(ctx context.Context, dbtx db.DBTX, userID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE user_id = $1 AND is_current = true)", userID).Scan(&exists)
	return exists, err
}

func walletExists(ctx context.Context, dbtx db.DBTX, wallet string) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE wallet = $1)", strings.ToLower(wallet)).Scan(&exists)
	return exists, err
}

func activeUserWalletExists(ctx context.Context, dbtx db.DBTX, wallet string) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM users WHERE wallet = $1 AND is_current = true AND is_deactivated = false)",
		strings.ToLower(wallet)).Scan(&exists)
	return exists, err
}

func developerAppExists(ctx context.Context, dbtx db.DBTX, address string) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM developer_apps WHERE address = $1 AND is_delete = false)", strings.ToLower(address)).Scan(&exists)
	return exists, err
}

func handleExists(ctx context.Context, dbtx db.DBTX, handleLC string) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE handle_lc = $1 AND is_current = true)", handleLC).Scan(&exists)
	return exists, err
}

// UserCreate returns the User Create handler.
func UserCreate() Handler { return &userCreateHandler{} }
