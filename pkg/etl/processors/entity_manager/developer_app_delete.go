package entity_manager

import (
	"context"
	"strings"
)

type developerAppDeleteHandler struct{}

func (h *developerAppDeleteHandler) EntityType() string { return EntityTypeDeveloperApp }
func (h *developerAppDeleteHandler) Action() string     { return ActionDelete }

func (h *developerAppDeleteHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateDeveloperAppDelete(ctx, params); err != nil {
		return err
	}
	return deleteDeveloperApp(ctx, params)
}

func validateDeveloperAppDelete(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	address := strings.ToLower(params.MetadataString("address"))
	if address == "" {
		return NewValidationError("address is required for developer app delete")
	}

	exists, err := developerAppExists(ctx, params.DBTX, address)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("developer app %s does not exist", address)
	}

	ownerID, err := getDeveloperAppOwner(ctx, params.DBTX, address)
	if err != nil {
		return err
	}
	if ownerID != params.UserID {
		return NewValidationError("developer app %s does not match user %d", address, params.UserID)
	}

	return nil
}

func deleteDeveloperApp(ctx context.Context, params *Params) error {
	address := strings.ToLower(params.MetadataString("address"))

	// Mark current row as not current
	_, err := params.DBTX.Exec(ctx,
		"UPDATE developer_apps SET is_current = false WHERE address = $1 AND is_current = true",
		address)
	if err != nil {
		return err
	}

	isPersonalAccess, createdAt, err := getDeveloperAppDetails(ctx, params.DBTX, address)
	if err != nil {
		return err
	}

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO developer_apps (
			address, user_id, name, description, is_personal_access,
			is_current, is_delete, created_at, updated_at, txhash, blocknumber
		) VALUES ($1, $2, '', NULL, $3, true, true, $4, $5, $6, $7)
	`,
		address,
		params.UserID,
		isPersonalAccess,
		createdAt,
		params.BlockTime,
		params.TxHash,
		params.BlockNumber,
	)
	return err
}

// DeveloperAppDelete returns the DeveloperApp Delete handler.
func DeveloperAppDelete() Handler { return &developerAppDeleteHandler{} }
