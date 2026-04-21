package entity_manager

import "context"

type encryptedEmailHandler struct{}

func (h *encryptedEmailHandler) EntityType() string { return EntityTypeEncryptedEmail }
func (h *encryptedEmailHandler) Action() string     { return ActionAddEmail }

func (h *encryptedEmailHandler) Handle(ctx context.Context, params *Params) error {
	if params.Metadata == nil {
		return NewValidationError("email metadata must be a dictionary")
	}

	ownerID, ok := params.MetadataInt64("email_owner_user_id")
	if !ok {
		return NewValidationError("missing required field: email_owner_user_id")
	}
	encryptedEmail := params.MetadataString("encrypted_email")
	if encryptedEmail == "" {
		return NewValidationError("missing required field: encrypted_email")
	}
	grantList, err := parseAccessGrants(params)
	if err != nil {
		return err
	}

	var exists bool
	err = params.DBTX.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM encrypted_emails WHERE email_owner_user_id = $1)",
		ownerID).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return nil // silently skip if already exists
	}

	_, err = params.DBTX.Exec(ctx,
		"INSERT INTO encrypted_emails (email_owner_user_id, encrypted_email) VALUES ($1, $2)",
		ownerID, encryptedEmail)
	if err != nil {
		return err
	}

	for _, g := range grantList {
		_, err = params.DBTX.Exec(ctx, `
			INSERT INTO email_access (email_owner_user_id, receiving_user_id, grantor_user_id, encrypted_key)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (email_owner_user_id, receiving_user_id, grantor_user_id) DO NOTHING
		`, ownerID, g.receivingUserID, g.grantorUserID, g.encryptedKey)
		if err != nil {
			return err
		}
	}

	return nil
}

type emailAccessHandler struct{}

func (h *emailAccessHandler) EntityType() string { return EntityTypeEmailAccess }
func (h *emailAccessHandler) Action() string     { return ActionUpdate }

func (h *emailAccessHandler) Handle(ctx context.Context, params *Params) error {
	if params.Metadata == nil {
		return NewValidationError("email metadata must be a dictionary")
	}

	ownerID, ok := params.MetadataInt64("email_owner_user_id")
	if !ok {
		return NewValidationError("missing required field: email_owner_user_id")
	}
	grantList, err := parseAccessGrants(params)
	if err != nil {
		return err
	}

	for _, g := range grantList {
		// Verify grantor has access before granting to another user
		var grantorHasAccess bool
		err := params.DBTX.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM email_access WHERE email_owner_user_id = $1 AND receiving_user_id = $2)",
			ownerID, g.grantorUserID).Scan(&grantorHasAccess)
		if err != nil {
			return err
		}
		if !grantorHasAccess {
			return NewValidationError("grantor %d does not have access to the email", g.grantorUserID)
		}

		_, err = params.DBTX.Exec(ctx, `
			INSERT INTO email_access (email_owner_user_id, receiving_user_id, grantor_user_id, encrypted_key)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (email_owner_user_id, receiving_user_id, grantor_user_id) DO NOTHING
		`, ownerID, g.receivingUserID, g.grantorUserID, g.encryptedKey)
		if err != nil {
			return err
		}
	}

	return nil
}

type accessGrant struct {
	receivingUserID int64
	grantorUserID   int64
	encryptedKey    string
}

// parseAccessGrants extracts and validates access_grants from metadata.
func parseAccessGrants(params *Params) ([]accessGrant, error) {
	grants, ok := params.MetadataJSON("access_grants")
	if !ok {
		return nil, NewValidationError("missing required field: access_grants")
	}
	grantSlice, ok := grants.([]any)
	if !ok {
		return nil, NewValidationError("access_grants must be a list")
	}

	result := make([]accessGrant, 0, len(grantSlice))
	for _, g := range grantSlice {
		gMap, ok := g.(map[string]any)
		if !ok {
			return nil, NewValidationError("each access grant must be a dict")
		}
		receivingRaw, ok := gMap["receiving_user_id"]
		if !ok {
			return nil, NewValidationError("each access grant must contain receiving_user_id")
		}
		grantorRaw, ok := gMap["grantor_user_id"]
		if !ok {
			return nil, NewValidationError("each access grant must contain grantor_user_id")
		}
		encKey, ok := gMap["encrypted_key"].(string)
		if !ok || encKey == "" {
			return nil, NewValidationError("each access grant must contain encrypted_key")
		}

		receivingID, ok := receivingRaw.(float64)
		if !ok {
			return nil, NewValidationError("receiving_user_id must be a number")
		}
		grantorID, ok := grantorRaw.(float64)
		if !ok {
			return nil, NewValidationError("grantor_user_id must be a number")
		}

		result = append(result, accessGrant{
			receivingUserID: int64(receivingID),
			grantorUserID:   int64(grantorID),
			encryptedKey:    encKey,
		})
	}
	return result, nil
}

func EncryptedEmailCreate() Handler { return &encryptedEmailHandler{} }
func EmailAccessUpdate() Handler    { return &emailAccessHandler{} }
