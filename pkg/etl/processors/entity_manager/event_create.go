package entity_manager

import (
	"context"
	"encoding/json"
	"time"
)

type eventCreateHandler struct{}

func (h *eventCreateHandler) EntityType() string { return EntityTypeEvent }
func (h *eventCreateHandler) Action() string     { return ActionCreate }

func (h *eventCreateHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateCreateEvent(ctx, params); err != nil {
		return err
	}

	eventType := params.MetadataString("event_type")
	entityType := params.MetadataString("entity_type")
	entityID, _ := params.MetadataInt64("entity_id")
	endDateStr := params.MetadataString("end_date")

	var eventDataJSON []byte
	if ed, ok := params.MetadataJSON("event_data"); ok {
		eventDataJSON, _ = json.Marshal(ed)
	}

	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO events (
			event_id, event_type, user_id, entity_type, entity_id,
			end_date, event_data, is_deleted,
			created_at, updated_at, txhash, blocknumber
		) VALUES ($1, $2, $3, $4, $5, $6, $7, false, $8, $8, $9, $10)
	`, params.EntityID, eventType, params.UserID, entityType, entityID,
		endDateStr, eventDataJSON, params.BlockTime, params.TxHash, params.BlockNumber)
	return err
}

func validateCreateEvent(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	exists, err := eventExists(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if exists {
		return NewValidationError("event %d already exists", params.EntityID)
	}

	eventType := params.MetadataString("event_type")
	if eventType == "" {
		return NewValidationError("missing required field: event_type")
	}
	endDateStr := params.MetadataString("end_date")
	if endDateStr == "" {
		return NewValidationError("missing required field: end_date")
	}

	endDate, err := time.Parse(time.RFC3339, endDateStr)
	if err != nil {
		endDate, err = time.Parse("2006-01-02T15:04:05", endDateStr)
		if err != nil {
			return NewValidationError("end_date is not a valid iso format")
		}
	}
	if endDate.Before(params.BlockTime) {
		return NewValidationError("end_date cannot be in the past")
	}

	userOK, err := userExists(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}
	if !userOK {
		return NewValidationError("user %d does not exist", params.UserID)
	}

	entityType := params.MetadataString("entity_type")
	entityID, hasEntityID := params.MetadataInt64("entity_id")
	if entityType == "track" && hasEntityID {
		trackOK, err := trackExists(ctx, params.DBTX, entityID)
		if err != nil {
			return err
		}
		if !trackOK {
			return NewValidationError("track %d does not exist", entityID)
		}
		ownerID, err := trackOwner(ctx, params.DBTX, entityID)
		if err != nil {
			return err
		}
		if ownerID != params.UserID {
			return NewValidationError("user %d is not the owner of track %d", params.UserID, entityID)
		}
	}

	if eventType == "remix_contest" {
		if !hasEntityID || entityType == "" {
			return NewValidationError("for remix competitions, entity_id and entity_type must be provided")
		}
		var contestExists bool
		err := params.DBTX.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM events WHERE entity_id = $1 AND event_type = 'remix_contest' AND is_deleted = false AND end_date > $2)",
			entityID, params.BlockTime).Scan(&contestExists)
		if err != nil {
			return err
		}
		if contestExists {
			return NewValidationError("an existing remix contest for entity_id %d already exists", entityID)
		}
		var remixOf []byte
		_ = params.DBTX.QueryRow(ctx,
			"SELECT remix_of FROM tracks WHERE track_id = $1 AND is_current = true LIMIT 1",
			entityID).Scan(&remixOf)
		if remixOf != nil && string(remixOf) != "null" && string(remixOf) != "" {
			return NewValidationError("track %d is a remix and cannot host a remix contest", entityID)
		}
	}

	return nil
}

func EventCreate() Handler { return &eventCreateHandler{} }
