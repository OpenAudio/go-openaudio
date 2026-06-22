package entity_manager

import (
	"context"
	"encoding/json"
	"fmt"
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
	if err != nil {
		return err
	}

	return insertEventRoute(ctx, params, entityID)
}

// insertEventRoute generates a slug for the event and inserts into event_routes.
// It also writes a backwards-compatible legacy route derived from the source
// track's current slug (with is_current=false) so old URLs keep resolving.
func insertEventRoute(ctx context.Context, params *Params, trackEntityID int64) error {
	// Derive title: prefer event_data["title"], fall back to "contest-<event_id>".
	title := ""
	if ed, ok := params.MetadataJSON("event_data"); ok && ed != nil {
		if m, ok := ed.(map[string]interface{}); ok {
			if t, ok := m["title"].(string); ok {
				title = t
			}
		}
	}
	if title == "" {
		title = fmt.Sprintf("contest-%d", params.EntityID)
	}

	slug, titleSlug, collisionID, err := GenerateEventSlugAndCollisionID(ctx, params.DBTX, params.UserID, params.EntityID, title)
	if err != nil {
		return err
	}

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO event_routes (
			slug, title_slug, collision_id, owner_id, event_id, is_current,
			blockhash, blocknumber, txhash
		) VALUES (
			$1, $2, $3, $4, $5, true,
			$6, $7, $8
		)
	`, slug, titleSlug, collisionID, params.UserID, params.EntityID,
		params.BlockHash, params.BlockNumber, params.TxHash)
	if err != nil {
		return err
	}

	// Write a legacy route from the source track's current slug (is_current=false)
	// so that the old URL /:handle/contest/:track-slug still resolves.
	if trackEntityID > 0 {
		var trackSlug string
		qErr := params.DBTX.QueryRow(ctx,
			"SELECT slug FROM track_routes WHERE track_id = $1 AND is_current = true LIMIT 1",
			trackEntityID).Scan(&trackSlug)
		if qErr == nil && trackSlug != "" && trackSlug != slug {
			// Insert legacy route only when it doesn't collide with the new slug.
			_, _ = params.DBTX.Exec(ctx, `
				INSERT INTO event_routes (
					slug, title_slug, collision_id, owner_id, event_id, is_current,
					blockhash, blocknumber, txhash
				) VALUES (
					$1, $2, 0, $3, $4, false,
					$5, $6, $7
				) ON CONFLICT (owner_id, slug) DO NOTHING
			`, trackSlug, trackSlug, params.UserID, params.EntityID,
				params.BlockHash, params.BlockNumber, params.TxHash)
		}
	}

	return nil
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
