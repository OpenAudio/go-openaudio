package entity_manager

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	CharacterLimitAppName        = 50
	CharacterLimitAppDescription = 160
)

// devAppImageURLRegexp mirrors the legacy indexer's is_fqdn check
// (discovery-provider src/utils/helpers.py): a developer app image_url is only
// persisted when it is a valid fully-qualified URL/host.
var devAppImageURLRegexp = regexp.MustCompile(`^(?:^|[ \t])((https?://)?(?:localhost|(cn[0-9]_creator-node_1:[0-9]+)|(audius-protocol-creator-node-[0-9])|(audius-protocol-discovery-provider-[0-9])|[\w-]+(?:\.[\w-]+)+)(:\d+)?(/\S*)?)$`)

// validatedAppImageURL returns the metadata image_url when it passes the FQDN
// check, else "" (persisted as NULL). Matches the legacy indexer, which sets
// image_url from metadata on both create and update (None when absent/invalid).
func validatedAppImageURL(params *Params) string {
	img := params.MetadataString("image_url")
	if img != "" && devAppImageURLRegexp.MatchString(img) {
		return img
	}
	return ""
}

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
	if utf8.RuneCountInString(name) > CharacterLimitAppName {
		return NewValidationError("name exceeds %d character limit", CharacterLimitAppName)
	}

	if desc := params.MetadataString("description"); desc != "" {
		if utf8.RuneCountInString(desc) > CharacterLimitAppDescription {
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
	return insertDeveloperAppWithState(ctx, params, false)
}

// insertDeveloperAppWithState writes an app carrying its deleted state.
// Production always passes false -- a client cannot create an already deleted
// app. Only the migration replays one.
func insertDeveloperAppWithState(ctx context.Context, params *Params, isDelete bool) error {
	address := strings.ToLower(params.MetadataString("address"))
	name := params.MetadataString("name")
	description := params.MetadataString("description")
	imageURL := validatedAppImageURL(params)
	isPersonalAccess := params.MetadataBoolOr("is_personal_access", false)

	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO developer_apps (
			address, user_id, name, description, image_url, is_personal_access,
			is_current, is_delete, created_at, updated_at, txhash, blocknumber
		) VALUES ($1, $2, $3, $4, $5, $6, true, $10, $7, $7, $8, $9)
	`,
		address,
		params.UserID,
		name,
		nullString(description),
		nullString(imageURL),
		isPersonalAccess,
		params.BlockTime,
		params.TxHash,
		params.BlockNumber,
		isDelete,
	)
	if err != nil {
		return err
	}

	// Index redirect_uris if provided.
	if uris, present, _ := extractRedirectURIs(params.Metadata); present && len(uris) > 0 {
		if err := replaceRedirectURIs(ctx, params.DBTX, address, uris); err != nil {
			return err
		}
	}
	return nil
}

// DeveloperAppCreate returns the DeveloperApp Create handler.
func DeveloperAppCreate() Handler { return &developerAppCreateHandler{} }
