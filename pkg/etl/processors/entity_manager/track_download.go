package entity_manager

import (
	"context"
	"encoding/json"
)

type trackDownloadHandler struct{}

func (h *trackDownloadHandler) EntityType() string { return EntityTypeTrack }
func (h *trackDownloadHandler) Action() string     { return ActionDownload }

func (h *trackDownloadHandler) Handle(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	trackID := params.EntityID

	exists, err := trackExists(ctx, params.DBTX, trackID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("track %d does not exist", trackID)
	}

	// Determine parent_track_id: if this is a stem, use the parent; otherwise same as track_id
	parentTrackID := trackID
	var stemOfRaw []byte
	err = params.DBTX.QueryRow(ctx,
		"SELECT stem_of FROM tracks WHERE track_id = $1 AND is_current = true",
		trackID).Scan(&stemOfRaw)
	if err == nil && stemOfRaw != nil {
		var stemOf map[string]any
		if json.Unmarshal(stemOfRaw, &stemOf) == nil {
			if pid, ok := stemOf["parent_track_id"]; ok {
				if pidFloat, ok := pid.(float64); ok {
					parentTrackID = int64(pidFloat)
				}
			}
		}
	}

	// Check for duplicate download
	var dup bool
	err = params.DBTX.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM track_downloads WHERE parent_track_id = $1 AND track_id = $2 AND txhash = $3)",
		parentTrackID, trackID, params.TxHash).Scan(&dup)
	if err != nil {
		return err
	}
	if dup {
		return nil // silently skip duplicate
	}

	city := params.MetadataString("city")
	region := params.MetadataString("region")
	country := params.MetadataString("country")

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO track_downloads (
			txhash, blocknumber, parent_track_id, track_id, user_id,
			city, region, country
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, params.TxHash, params.BlockNumber, parentTrackID, trackID, params.UserID,
		nilIfEmpty(city), nilIfEmpty(region), nilIfEmpty(country))
	return err
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func TrackDownload() Handler { return &trackDownloadHandler{} }
