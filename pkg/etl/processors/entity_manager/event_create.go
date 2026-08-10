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
	return insertEvent(ctx, params)
}

// insertEvent writes the event row. The genesis migration handler shares it, so
// the two paths differ only in which validations run ahead of the write.
func insertEvent(ctx context.Context, params *Params) error {
	eventType := params.MetadataString("event_type")
	entityType := params.MetadataString("entity_type")
	entityID, _ := params.MetadataInt64("entity_id")
	endDateStr := params.MetadataString("end_date")

	var eventDataJSON []byte
	if ed, ok := params.MetadataJSON("event_data"); ok {
		eventDataJSON, _ = json.Marshal(ed)
	}

	// blockhash is NOT NULL on production's `events` table (our migration
	// has a DEFAULT '' that masks the omission locally — see #305 follow-up).
	// Always pass params.BlockHash so we don't rely on the default.
	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO events (
			event_id, event_type, user_id, entity_type, entity_id,
			end_date, event_data, is_deleted,
			created_at, updated_at, txhash, blockhash, blocknumber
		) VALUES ($1, $2, $3, $4, $5, $6, $7, false, $8, $8, $9, $10, $11)
	`, params.EntityID, eventType, params.UserID, entityType, entityID,
		endDateStr, eventDataJSON, params.BlockTime, params.TxHash, params.BlockHash, params.BlockNumber)
	return err
}

// validateCreateEvent validates a new event submitted by a client.
//
// It runs in two parts. validateEventCreateShape holds the checks an event must
// satisfy however it arrives — signer authority, idempotency, required fields,
// and the rows it references. The checks below it are live-submission policy:
// they ask whether this contest may open *now*, a question a replay of
// already-concluded state cannot answer. See migratedEventCreate.
func validateCreateEvent(ctx context.Context, params *Params) error {
	endDate, err := validateEventCreateShape(ctx, params)
	if err != nil {
		return err
	}

	if endDate.Before(params.BlockTime) {
		return NewValidationError("end_date cannot be in the past")
	}

	if params.MetadataString("event_type") == "remix_contest" {
		entityID, _ := params.MetadataInt64("entity_id")
		return validateRemixContestOpening(ctx, params, entityID)
	}

	return nil
}

// validateEventCreateShape checks that the event is well formed, is not already
// indexed, is signed by someone who may act for its user, and hangs off rows
// that exist. It returns the parsed end_date for the caller's policy checks.
func validateEventCreateShape(ctx context.Context, params *Params) (time.Time, error) {
	var endDate time.Time

	if err := ValidateSigner(ctx, params); err != nil {
		return endDate, err
	}

	exists, err := eventExists(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return endDate, err
	}
	if exists {
		return endDate, NewValidationError("event %d already exists", params.EntityID)
	}

	eventType := params.MetadataString("event_type")
	if eventType == "" {
		return endDate, NewValidationError("missing required field: event_type")
	}
	endDateStr := params.MetadataString("end_date")
	if endDateStr == "" {
		return endDate, NewValidationError("missing required field: end_date")
	}
	endDate, err = parseEventEndDate(endDateStr)
	if err != nil {
		return endDate, err
	}

	userOK, err := userExists(ctx, params.DBTX, params.UserID)
	if err != nil {
		return endDate, err
	}
	if !userOK {
		return endDate, NewValidationError("user %d does not exist", params.UserID)
	}

	entityType := params.MetadataString("entity_type")
	entityID, hasEntityID := params.MetadataInt64("entity_id")
	if entityType == "track" && hasEntityID {
		trackOK, err := trackExists(ctx, params.DBTX, entityID)
		if err != nil {
			return endDate, err
		}
		if !trackOK {
			return endDate, NewValidationError("track %d does not exist", entityID)
		}
		ownerID, err := trackOwner(ctx, params.DBTX, entityID)
		if err != nil {
			return endDate, err
		}
		if ownerID != params.UserID {
			return endDate, NewValidationError("user %d is not the owner of track %d", params.UserID, entityID)
		}
	}

	if eventType == "remix_contest" && (!hasEntityID || entityType == "") {
		return endDate, NewValidationError("for remix competitions, entity_id and entity_type must be provided")
	}

	return endDate, nil
}

// validateRemixContestOpening enforces the two rules that govern opening a
// contest at the current block time: an entity may host only one contest that
// is still running, and a remix may not host one at all.
func validateRemixContestOpening(ctx context.Context, params *Params, entityID int64) error {
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

	return nil
}

// parseEventEndDate accepts the two shapes end_date arrives in: RFC3339, and
// the zone-less form some clients send.
func parseEventEndDate(s string) (time.Time, error) {
	endDate, err := time.Parse(time.RFC3339, s)
	if err != nil {
		endDate, err = time.Parse("2006-01-02T15:04:05", s)
		if err != nil {
			return time.Time{}, NewValidationError("end_date is not a valid iso format")
		}
	}
	return endDate, nil
}

func EventCreate() Handler { return &eventCreateHandler{} }
