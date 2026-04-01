package entity_manager

import "context"

type tipReactionHandler struct{}

func (h *tipReactionHandler) EntityType() string { return EntityTypeTip }
func (h *tipReactionHandler) Action() string     { return ActionUpdate }

func (h *tipReactionHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateTipReaction(ctx, params); err != nil {
		return err
	}

	reactedTo := params.MetadataString("reacted_to")
	reactionValue, _ := params.MetadataInt64("reaction_value")

	// Look up the tip sender's wallet
	var senderUserID int64
	err := params.DBTX.QueryRow(ctx,
		"SELECT sender_user_id FROM user_tips WHERE signature = $1 AND receiver_user_id = $2 LIMIT 1",
		reactedTo, params.UserID).Scan(&senderUserID)
	if err != nil {
		return NewValidationError("tip with signature %s for user %d not found", reactedTo, params.UserID)
	}

	senderWallet, err := getUserWallet(ctx, params.DBTX, senderUserID)
	if err != nil || senderWallet == "" {
		return NewValidationError("tip sender %d wallet not found", senderUserID)
	}

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO reactions (reaction_value, sender_wallet, reaction_type, reacted_to, timestamp, blocknumber)
		VALUES ($1, $2, 'tip', $3, $4, $5)
	`, reactionValue, senderWallet, reactedTo, params.BlockTime, params.BlockNumber)
	return err
}

func validateTipReaction(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	reactedTo := params.MetadataString("reacted_to")
	if reactedTo == "" {
		return NewValidationError("reacted_to is required")
	}

	reactionValue, ok := params.MetadataInt64("reaction_value")
	if !ok {
		return NewValidationError("reaction_value is required")
	}
	if reactionValue < 1 || reactionValue > 4 {
		return NewValidationError("reaction_value must be between 1 and 4")
	}

	return nil
}

func TipReaction() Handler { return &tipReactionHandler{} }
