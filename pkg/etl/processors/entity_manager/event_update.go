package entity_manager

import (
	"context"
	"encoding/json"
	"time"
)

type eventUpdateHandler struct{}

func (h *eventUpdateHandler) EntityType() string { return EntityTypeEvent }
func (h *eventUpdateHandler) Action() string     { return ActionUpdate }

func (h *eventUpdateHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateUpdateEvent(ctx, params); err != nil {
		return err
	}

	// Build a single UPDATE with all changed fields
	endDateStr := params.MetadataString("end_date")
	var endDate *string
	if endDateStr != "" {
		endDate = &endDateStr
	}

	var eventDataJSON []byte
	if ed, ok := params.MetadataJSON("event_data"); ok {
		eventDataJSON, _ = json.Marshal(ed)
	}

	_, err := params.DBTX.Exec(ctx, `
		UPDATE events SET
			end_date = COALESCE($1, end_date),
			event_data = COALESCE($2, event_data),
			updated_at = $3, txhash = $4, blocknumber = $5
		WHERE event_id = $6
	`, endDate, eventDataJSON, params.BlockTime, params.TxHash, params.BlockNumber, params.EntityID)
	return err
}

func validateUpdateEvent(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	exists, err := eventExists(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("cannot update event %d that does not exist", params.EntityID)
	}

	ownerID, err := eventOwner(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if ownerID != params.UserID {
		return NewValidationError("only event owner can update event %d", params.EntityID)
	}

	endDateStr := params.MetadataString("end_date")
	if endDateStr != "" {
		endDate, err := time.Parse(time.RFC3339, endDateStr)
		if err != nil {
			endDate, err = time.Parse("2006-01-02T15:04:05", endDateStr)
			if err != nil {
				return NewValidationError("end_date is not a valid iso format")
			}
		}

		// For remix contests, new end_date must be >= current end_date
		evtType, curEndDate, err := eventTypeAndEndDate(ctx, params.DBTX, params.EntityID)
		if err != nil {
			return err
		}
		if evtType == "remix_contest" && curEndDate != nil {
			if endDate.Before(*curEndDate) {
				return NewValidationError("end_date cannot be before the current end_date of the remix contest")
			}
		} else if endDate.Before(params.BlockTime) {
			return NewValidationError("end_date cannot be in the past")
		}
	}

	return nil
}

func EventUpdate() Handler { return &eventUpdateHandler{} }
