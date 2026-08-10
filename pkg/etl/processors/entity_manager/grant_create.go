package entity_manager

import (
	"context"
	"strings"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
)

type grantCreateHandler struct{}

func (h *grantCreateHandler) EntityType() string { return EntityTypeGrant }
func (h *grantCreateHandler) Action() string     { return ActionCreate }

func (h *grantCreateHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateGrantCreate(ctx, params); err != nil {
		return err
	}
	return insertGrant(ctx, params)
}

func validateGrantCreate(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	granteeAddress := strings.ToLower(params.MetadataString("grantee_address"))
	if granteeAddress == "" {
		return NewValidationError("grantee_address is required for grant creation")
	}

	// Grantee must be a developer app or user wallet
	isApp, err := developerAppExists(ctx, params.DBTX, granteeAddress)
	if err != nil {
		return err
	}
	isUser, err := walletExists(ctx, params.DBTX, granteeAddress)
	if err != nil {
		return err
	}
	if !isApp && !isUser {
		return NewValidationError("grantee %s is not a developer app or user wallet", granteeAddress)
	}

	// Check no active grant exists between this user and grantee
	active, err := activeGrantExists(ctx, params.DBTX, granteeAddress, params.UserID)
	if err != nil {
		return err
	}
	if active {
		return NewValidationError("active grant already exists for grantee %s from user %d", granteeAddress, params.UserID)
	}

	return nil
}

// insertGrant writes a newly created grant. It passes the zero grantState, so a
// live client cannot self-assign either flag through metadata: a new grant is
// always unrevoked, and its approval is derived from the grantee's type exactly
// as before.
func insertGrant(ctx context.Context, params *Params) error {
	return insertGrantWithState(ctx, params, grantState{})
}

// grantState carries the flags an inserted grant may already hold. Only the
// genesis migration supplies a non-zero value; see the migration handlers.
type grantState struct {
	IsRevoked bool

	// IsApproved is three-valued and nil means "derive it", which is what the
	// production path passes. A user-to-user grant is approved by a separate
	// Grant/Approve transaction, so a live create has nothing to carry -- but a
	// migrated create is the account's final state, and the approval it records
	// cannot be reconstructed from the grantee's type. 493 approved manager
	// grants on a production clone derive to NULL, and ValidateSigner refuses a
	// non-app grantee whose grant is not approved, so each of those managers
	// would silently lose access to the account they manage.
	IsApproved *bool
}

// insertGrantWithState writes a grant carrying its revoked and approved state.
func insertGrantWithState(ctx context.Context, params *Params, state grantState) error {
	granteeAddress := strings.ToLower(params.MetadataString("grantee_address"))

	// Determine is_approved: the caller's value if it supplied one, otherwise
	// true if the grantee is an app and nil if user-to-user.
	isApproved := state.IsApproved
	if isApproved == nil {
		isApp, _ := developerAppExists(ctx, params.DBTX, granteeAddress)
		if isApp {
			t := true
			isApproved = &t
		}
	}

	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO grants (
			grantee_address, user_id, is_revoked, is_current, is_approved,
			created_at, updated_at, txhash, blocknumber
		) VALUES ($1, $2, $7, true, $3, $4, $4, $5, $6)
	`,
		granteeAddress,
		params.UserID,
		isApproved,
		params.BlockTime,
		params.TxHash,
		params.BlockNumber,
		state.IsRevoked,
	)
	return err
}

func activeGrantExists(ctx context.Context, dbtx db.DBTX, granteeAddress string, userID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM grants WHERE grantee_address = $1 AND user_id = $2 AND is_current = true AND is_revoked = false)",
		granteeAddress, userID).Scan(&exists)
	return exists, err
}

// GrantCreate returns the Grant Create handler.
func GrantCreate() Handler { return &grantCreateHandler{} }
