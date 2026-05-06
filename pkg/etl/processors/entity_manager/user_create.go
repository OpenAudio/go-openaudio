package entity_manager

import (
	"context"
	"strings"

	"github.com/OpenAudio/go-openaudio/etl/db"
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

func insertUser(ctx context.Context, params *Params) error {
	handle := nullString(params.MetadataString("handle"))
	var handleLC any
	if h := params.MetadataString("handle"); h != "" {
		handleLC = strings.ToLower(h)
	}

	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO users (
			user_id, handle, handle_lc, wallet, name, bio, location,
			profile_picture, profile_picture_sizes, cover_photo, cover_photo_sizes,
			is_current, is_verified, is_deactivated, is_available, is_storage_v2,
			created_at, updated_at, txhash, blockhash, blocknumber
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11,
			true, false, false, true, true,
			$12, $12, $13, $14, $15
		)
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
		params.BlockTime,
		params.TxHash,
		params.BlockHash,
		params.BlockNumber,
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
