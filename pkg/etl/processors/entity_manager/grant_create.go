package entity_manager

import (
	"context"
	"strings"

	"github.com/OpenAudio/go-openaudio/etl/db"
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

func insertGrant(ctx context.Context, params *Params) error {
	granteeAddress := strings.ToLower(params.MetadataString("grantee_address"))

	// Determine is_approved: true if grantee is an app, nil if user-to-user
	isApp, _ := developerAppExists(ctx, params.DBTX, granteeAddress)
	var isApproved *bool
	if isApp {
		t := true
		isApproved = &t
	}

	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO grants (
			grantee_address, user_id, is_revoked, is_current, is_approved,
			created_at, updated_at, txhash, blocknumber
		) VALUES ($1, $2, false, true, $3, $4, $4, $5, $6)
	`,
		granteeAddress,
		params.UserID,
		isApproved,
		params.BlockTime,
		params.TxHash,
		params.BlockNumber,
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
