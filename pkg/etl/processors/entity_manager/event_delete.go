package entity_manager

import "context"

type eventDeleteHandler struct{}

func (h *eventDeleteHandler) EntityType() string { return EntityTypeEvent }
func (h *eventDeleteHandler) Action() string     { return ActionDelete }

func (h *eventDeleteHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateDeleteEvent(ctx, params); err != nil {
		return err
	}

	_, err := params.DBTX.Exec(ctx, `
		UPDATE events SET is_deleted = true, updated_at = $1, txhash = $2, blocknumber = $3
		WHERE event_id = $4
	`, params.BlockTime, params.TxHash, params.BlockNumber, params.EntityID)
	return err
}

func validateDeleteEvent(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	exists, err := eventExists(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("cannot delete event %d that does not exist", params.EntityID)
	}

	ownerID, err := eventOwner(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if ownerID != params.UserID {
		return NewValidationError("only event owner can delete event %d", params.EntityID)
	}

	return nil
}

func EventDelete() Handler { return &eventDeleteHandler{} }
