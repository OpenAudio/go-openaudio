package entity_manager

import (
	"context"
	"strings"
)

const (
	CharacterLimitAppName        = 50
	CharacterLimitAppDescription = 160
)

type developerAppCreateHandler struct{}

func (h *developerAppCreateHandler) EntityType() string { return EntityTypeDeveloperApp }
func (h *developerAppCreateHandler) Action() string     { return ActionCreate }

func (h *developerAppCreateHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateDeveloperAppCreate(ctx, params); err != nil {
		return err
	}
	return insertDeveloperApp(ctx, params)
}

func validateDeveloperAppCreate(ctx context.Context, params *Params) error {
	if params.Metadata == nil {
		return NewValidationError("metadata is required for developer app creation")
	}

	name := params.MetadataString("name")
	if name == "" {
		return NewValidationError("name is required for developer app")
	}
	if len(name) > CharacterLimitAppName {
		return NewValidationError("name exceeds %d character limit", CharacterLimitAppName)
	}

	if desc := params.MetadataString("description"); desc != "" {
		if len(desc) > CharacterLimitAppDescription {
			return NewValidationError("description exceeds %d character limit", CharacterLimitAppDescription)
		}
	}

	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	address := strings.ToLower(params.MetadataString("address"))
	if address == "" {
		return NewValidationError("address is required for developer app")
	}

	// Address must not already be a developer app
	exists, err := developerAppExists(ctx, params.DBTX, address)
	if err != nil {
		return err
	}
	if exists {
		return NewValidationError("developer app %s already exists", address)
	}

	// Address must not be an existing user wallet
	walletUsed, err := walletExists(ctx, params.DBTX, address)
	if err != nil {
		return err
	}
	if walletUsed {
		return NewValidationError("address %s is already a user wallet", address)
	}

	return nil
}

func insertDeveloperApp(ctx context.Context, params *Params) error {
	address := strings.ToLower(params.MetadataString("address"))
	name := params.MetadataString("name")
	description := params.MetadataString("description")
	isPersonalAccess := params.MetadataBoolOr("is_personal_access", false)

	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO developer_apps (
			address, user_id, name, description, is_personal_access,
			is_current, is_delete, created_at, updated_at, txhash, blocknumber
		) VALUES ($1, $2, $3, $4, $5, true, false, $6, $6, $7, $8)
	`,
		address,
		params.UserID,
		name,
		nullString(description),
		isPersonalAccess,
		params.BlockTime,
		params.TxHash,
		params.BlockNumber,
	)
	return err
}

// DeveloperAppCreate returns the DeveloperApp Create handler.
func DeveloperAppCreate() Handler { return &developerAppCreateHandler{} }
