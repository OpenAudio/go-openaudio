package entity_manager

import (
	"context"
	"strings"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

type developerAppUpdateHandler struct{}

func (h *developerAppUpdateHandler) EntityType() string { return EntityTypeDeveloperApp }
func (h *developerAppUpdateHandler) Action() string     { return ActionUpdate }

func (h *developerAppUpdateHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateDeveloperAppUpdate(ctx, params); err != nil {
		return err
	}
	return updateDeveloperApp(ctx, params)
}

func validateDeveloperAppUpdate(ctx context.Context, params *Params) error {
	if params.Metadata == nil {
		return NewValidationError("metadata is required for developer app update")
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
		return NewValidationError("address is required for developer app update")
	}

	// App must exist
	exists, err := developerAppExists(ctx, params.DBTX, address)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("developer app %s does not exist", address)
	}

	// Owner must match
	ownerID, err := getDeveloperAppOwner(ctx, params.DBTX, address)
	if err != nil {
		return err
	}
	if ownerID != params.UserID {
		return NewValidationError("developer app %s does not match user %d", address, params.UserID)
	}

	return nil
}

func updateDeveloperApp(ctx context.Context, params *Params) error {
	address := strings.ToLower(params.MetadataString("address"))
	name := params.MetadataString("name")
	description := params.MetadataString("description")

	// Mark current row as not current
	_, err := params.DBTX.Exec(ctx,
		"UPDATE developer_apps SET is_current = false WHERE address = $1 AND is_current = true",
		address)
	if err != nil {
		return err
	}

	// Load existing for fields we don't update
	isPersonalAccess, createdAt, err := getDeveloperAppDetails(ctx, params.DBTX, address)
	if err != nil {
		return err
	}

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO developer_apps (
			address, user_id, name, description, is_personal_access,
			is_current, is_delete, created_at, updated_at, txhash, blocknumber
		) VALUES ($1, $2, $3, $4, $5, true, false, $6, $7, $8, $9)
	`,
		address,
		params.UserID,
		name,
		nullString(description),
		isPersonalAccess,
		createdAt,
		params.BlockTime,
		params.TxHash,
		params.BlockNumber,
	)
	return err
}

func getDeveloperAppOwner(ctx context.Context, dbtx db.DBTX, address string) (int64, error) {
	var ownerID int64
	err := dbtx.QueryRow(ctx,
		"SELECT user_id FROM developer_apps WHERE address = $1 AND is_delete = false LIMIT 1",
		address).Scan(&ownerID)
	return ownerID, err
}

func getDeveloperAppDetails(ctx context.Context, dbtx db.DBTX, address string) (isPersonalAccess bool, createdAt interface{}, err error) {
	err = dbtx.QueryRow(ctx,
		"SELECT is_personal_access, created_at FROM developer_apps WHERE address = $1 ORDER BY created_at LIMIT 1",
		address).Scan(&isPersonalAccess, &createdAt)
	return
}

// DeveloperAppUpdate returns the DeveloperApp Update handler.
func DeveloperAppUpdate() Handler { return &developerAppUpdateHandler{} }
