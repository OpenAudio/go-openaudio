package entity_manager

import (
	"context"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

// --- Grant Delete (Revoke) ---

type grantDeleteHandler struct{}

func (h *grantDeleteHandler) EntityType() string { return EntityTypeGrant }
func (h *grantDeleteHandler) Action() string     { return ActionDelete }

func (h *grantDeleteHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateGrantDelete(ctx, params); err != nil {
		return err
	}
	return revokeGrant(ctx, params)
}

func validateGrantDelete(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	granteeAddress := strings.ToLower(params.MetadataString("grantee_address"))
	if granteeAddress == "" {
		return NewValidationError("grantee_address is required for grant revoke")
	}

	active, err := activeGrantExists(ctx, params.DBTX, granteeAddress, params.UserID)
	if err != nil {
		return err
	}
	if !active {
		return NewValidationError("no active grant for grantee %s from user %d", granteeAddress, params.UserID)
	}

	return nil
}

func revokeGrant(ctx context.Context, params *Params) error {
	granteeAddress := strings.ToLower(params.MetadataString("grantee_address"))
	return setGrantRevoked(ctx, params.DBTX, granteeAddress, params.UserID, nil, true, params.BlockTime, params.TxHash, params.BlockNumber)
}

// --- Grant Approve ---

type grantApproveHandler struct{}

func (h *grantApproveHandler) EntityType() string { return EntityTypeGrant }
func (h *grantApproveHandler) Action() string     { return ActionApprove }

func (h *grantApproveHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateGrantApprove(ctx, params); err != nil {
		return err
	}
	return approveGrant(ctx, params)
}

func validateGrantApprove(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	granteeAddress := strings.ToLower(params.MetadataString("grantee_address"))
	if granteeAddress == "" {
		return NewValidationError("grantee_address is required for grant approve")
	}

	grantor, ok := params.MetadataInt64("grantor_user_id")
	if !ok {
		return NewValidationError("grantor_user_id is required for grant approve")
	}

	grant, err := getActiveGrant(ctx, params.DBTX, granteeAddress, grantor)
	if err != nil {
		return NewValidationError("grant not found for grantee %s from user %d", granteeAddress, grantor)
	}
	if grant.isRevoked {
		return NewValidationError("grant is already revoked")
	}
	if grant.isApproved != nil && *grant.isApproved {
		return NewValidationError("grant is already approved")
	}

	return nil
}

func approveGrant(ctx context.Context, params *Params) error {
	granteeAddress := strings.ToLower(params.MetadataString("grantee_address"))
	grantor, _ := params.MetadataInt64("grantor_user_id")
	approved := true
	return setGrantRevoked(ctx, params.DBTX, granteeAddress, grantor, &approved, false, params.BlockTime, params.TxHash, params.BlockNumber)
}

// --- Grant Reject ---

type grantRejectHandler struct{}

func (h *grantRejectHandler) EntityType() string { return EntityTypeGrant }
func (h *grantRejectHandler) Action() string     { return ActionReject }

func (h *grantRejectHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateGrantReject(ctx, params); err != nil {
		return err
	}
	return rejectGrant(ctx, params)
}

func validateGrantReject(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	granteeAddress := strings.ToLower(params.MetadataString("grantee_address"))
	if granteeAddress == "" {
		return NewValidationError("grantee_address is required for grant reject")
	}

	grantor, ok := params.MetadataInt64("grantor_user_id")
	if !ok {
		return NewValidationError("grantor_user_id is required for grant reject")
	}

	grant, err := getActiveGrant(ctx, params.DBTX, granteeAddress, grantor)
	if err != nil {
		return NewValidationError("grant not found for grantee %s from user %d", granteeAddress, grantor)
	}
	if grant.isRevoked {
		return NewValidationError("grant is already revoked")
	}
	if grant.isApproved != nil && *grant.isApproved {
		return NewValidationError("grant is already approved")
	}

	return nil
}

func rejectGrant(ctx context.Context, params *Params) error {
	granteeAddress := strings.ToLower(params.MetadataString("grantee_address"))
	grantor, _ := params.MetadataInt64("grantor_user_id")
	approved := false
	return setGrantRevoked(ctx, params.DBTX, granteeAddress, grantor, &approved, true, params.BlockTime, params.TxHash, params.BlockNumber)
}

// --- shared ---

type grantRow struct {
	isRevoked  bool
	isApproved *bool
	createdAt  time.Time
}

func getActiveGrant(ctx context.Context, dbtx db.DBTX, granteeAddress string, userID int64) (*grantRow, error) {
	var g grantRow
	err := dbtx.QueryRow(ctx,
		"SELECT is_revoked, is_approved, created_at FROM grants WHERE grantee_address = $1 AND user_id = $2 AND is_current = true LIMIT 1",
		granteeAddress, userID).Scan(&g.isRevoked, &g.isApproved, &g.createdAt)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func setGrantRevoked(ctx context.Context, dbtx db.DBTX, granteeAddress string, userID int64, isApproved *bool, isRevoked bool, blockTime time.Time, txHash string, blockNumber int64) error {
	// Load existing for created_at
	grant, err := getActiveGrant(ctx, dbtx, granteeAddress, userID)
	if err != nil {
		return err
	}

	// Mark current as not current
	_, err = dbtx.Exec(ctx,
		"UPDATE grants SET is_current = false WHERE grantee_address = $1 AND user_id = $2 AND is_current = true",
		granteeAddress, userID)
	if err != nil {
		return err
	}

	// Determine is_approved
	approved := isApproved
	if approved == nil {
		approved = grant.isApproved
	}

	_, err = dbtx.Exec(ctx, `
		INSERT INTO grants (
			grantee_address, user_id, is_revoked, is_current, is_approved,
			created_at, updated_at, txhash, blocknumber
		) VALUES ($1, $2, $3, true, $4, $5, $6, $7, $8)
	`,
		granteeAddress,
		userID,
		isRevoked,
		approved,
		grant.createdAt,
		blockTime,
		txHash,
		blockNumber,
	)
	return err
}

// GrantDelete returns the Grant Delete (Revoke) handler.
func GrantDelete() Handler { return &grantDeleteHandler{} }

// GrantApprove returns the Grant Approve handler.
func GrantApprove() Handler { return &grantApproveHandler{} }

// GrantReject returns the Grant Reject handler.
func GrantReject() Handler { return &grantRejectHandler{} }
