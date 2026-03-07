package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

func (s *Server) validateManageEntity(_ context.Context, stx *v1.SignedTransaction) (proto.Message, error) {
	manageEntity := stx.GetManageEntity()
	if manageEntity == nil {
		return nil, errors.New("not manage entity")
	}
	return manageEntity, nil
}

// access_authorities are wallet addresses that can sign to authorize stream access (programmable distribution)
type trackMetadata struct {
	CID               string   `json:"cid"`
	AccessAuthorities []string `json:"access_authorities"`
	Data              struct {
		TrackCid string `json:"track_cid"`
	} `json:"data"`
}

func (s *Server) finalizeManageEntity(ctx context.Context, stx *v1.SignedTransaction) (proto.Message, error) {
	tx, err := s.validateManageEntity(ctx, stx)
	if err != nil {
		return nil, fmt.Errorf("invalid manage entity: %v", err)
	}

	manageEntity := tx.(*v1.ManageEntityLegacy)

	// Populate sound_recordings and management_keys for Track Create/Update
	if strings.EqualFold(manageEntity.EntityType, "Track") &&
		(strings.EqualFold(manageEntity.Action, "Create") || strings.EqualFold(manageEntity.Action, "Update")) {
		if err := s.processTrackManageEntity(ctx, manageEntity); err != nil {
			s.logger.Error("failed to process track manage entity", zap.Error(err),
				zap.String("entity_type", manageEntity.EntityType),
				zap.Int64("entity_id", manageEntity.EntityId))
			// Don't fail the tx - allow it to commit; log and continue
		}
	}

	return manageEntity, nil
}

func (s *Server) processTrackManageEntity(ctx context.Context, me *v1.ManageEntityLegacy) error {
	var meta trackMetadata
	if err := json.Unmarshal([]byte(me.Metadata), &meta); err != nil {
		return fmt.Errorf("parse track metadata: %w", err)
	}

	signers := meta.AccessAuthorities
	trackID := strconv.FormatInt(me.EntityId, 10)
	q := s.getDb()

	if len(signers) == 0 {
		// access_authorities null/empty: ungate the track by removing management keys
		// (no track_cid needed, likely an update)
		if err := q.DeleteManagementKeysByTrackID(ctx, trackID); err != nil {
			return fmt.Errorf("delete management_keys: %w", err)
		}
		s.invalidateTrackAccessCache(trackID)
		return nil
	}

	cid := meta.Data.TrackCid
	if cid == "" {
		return fmt.Errorf("track metadata missing track_cid in data")
	}

	// These operations run in the block's pg tx (getDb returns WithTx(onGoingBlock)).
	// Replace existing rows (handles both Create and Update)
	if err := q.DeleteSoundRecordingsByTrackID(ctx, trackID); err != nil {
		return fmt.Errorf("delete sound_recordings: %w", err)
	}
	if err := q.DeleteManagementKeysByTrackID(ctx, trackID); err != nil {
		return fmt.Errorf("delete management_keys: %w", err)
	}

	soundRecordingID := fmt.Sprintf("sr_%s", trackID)
	if err := q.InsertSoundRecording(ctx, db.InsertSoundRecordingParams{
		SoundRecordingID: soundRecordingID,
		TrackID:          trackID,
		Cid:              cid,
		EncodingDetails:  pgtype.Text{},
	}); err != nil {
		return fmt.Errorf("insert sound_recording: %w", err)
	}

	for _, addr := range signers {
		if err := q.InsertManagementKey(ctx, db.InsertManagementKeyParams{
			TrackID: trackID,
			Address: addr,
		}); err != nil {
			return fmt.Errorf("insert management_key: %w", err)
		}
	}

	s.invalidateTrackAccessCache(trackID)
	return nil
}
