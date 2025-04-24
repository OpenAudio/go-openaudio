package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/core/db"
	"github.com/AudiusProject/audiusd/pkg/core/gen/core_proto"
	adx "github.com/AudiusProject/audiusd/pkg/core/gen/core_proto/audiusddex/v1beta1"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"
)

func (s *Server) isValidReleaseTx(ctx context.Context, tx *core_proto.SignedTransaction) error {
	if !s.config.ERNAccessControlEnabled {
		return errors.New("ERN feature disabled")
	}

	ern := tx.GetRelease()
	if ern == nil {
		return errors.New("Empty release in signed tx")
	}

	bodyBytes, err := proto.Marshal(ern)
	if err != nil {
		return fmt.Errorf("could not marshal release tx body into bytes: %v", err)
	}
	pubkey, _, err := common.EthRecover(tx.GetSignature(), bodyBytes)
	if err != nil {
		return fmt.Errorf("could not recover release tx signer: %v", err)
	}

	if ern.ReleaseHeader == nil {
		return errors.New("Empty release header")
	}
	if ern.ReleaseHeader.Sender == nil {
		return errors.New("Empty release sender")
	}
	if !bytes.Equal(crypto.CompressPubkey(pubkey), ern.ReleaseHeader.Sender.PubKey) {
		return errors.New("Sender and signer do not match")
	}

	if ern.ReleaseList == nil || len(ern.ReleaseList) == 0 {
		return errors.New("Empty release list")
	}
	if ern.ResourceList == nil || len(ern.ResourceList) == 0 {
		return errors.New("Empty resource list")
	}

	soundRecordings := make(map[string]*adx.SoundRecording)

	for _, resource := range ern.ResourceList {
		if resource.ResourceReference == "" {
			return errors.New("No resource reference associated with sound recording")
		}
		if resource.GetImage() != nil {
		} else if sr := resource.GetSoundRecording(); sr != nil {
			if sr.Id == nil {
				return errors.New("Empty sound recording id")
			}
			if _, ok := soundRecordings[resource.ResourceReference]; ok {
				return errors.New("Non-unique resource reference associated with sound recording")
			}
			soundRecordings[resource.ResourceReference] = sr
		} else {
			s.logger.Warningf("Unsupported resource type %v", resource.GetResource())
		}
	}

	for _, release := range ern.ReleaseList {
		if tr := release.GetTrackRelease(); tr != nil {
			if tr.ReleaseId == nil {
				return errors.New("Empty release ID for track")
			}
			if _, ok := soundRecordings[tr.ReleaseResourceReference]; !ok {
				return fmt.Errorf("No existing resource reference '%s' for track", tr.ReleaseResourceReference)
			}
		}
	}

	return nil
}

func (s *Server) finalizeRelease(ctx context.Context, tx *core_proto.SignedTransaction, txHash string) (*core_proto.SignedTransaction, error) {
	if err := s.isValidReleaseTx(ctx, tx); err != nil {
		return nil, err
	}
	qtx := s.getDb()
	ern := tx.GetRelease()

	soundRecordings := make(map[string]*adx.SoundRecording)

	for _, resource := range ern.ResourceList {
		if sr := resource.GetSoundRecording(); sr != nil {
			soundRecordings[resource.ResourceReference] = sr
		}
	}
	for _, release := range ern.ReleaseList {
		if tr := release.GetTrackRelease(); tr != nil {
			var id string
			if tr.ReleaseId.Grid != "" {
				id = tr.ReleaseId.Grid
			} else if tr.ReleaseId.Isrc != "" {
				id = tr.ReleaseId.Isrc
			} else if tr.ReleaseId.Icpn != "" {
				id = tr.ReleaseId.Icpn
			}
			if err := qtx.InsertTrackId(ctx, id); err != nil {
				return nil, fmt.Errorf("Could not create new track release: %v", err)
			}

			sr := soundRecordings[tr.ReleaseResourceReference]
			if err := qtx.InsertSoundRecording(ctx, db.InsertSoundRecordingParams{
				SoundRecordingID: sr.Id.Isrc,
				TrackID:          id,
				Cid:              sr.Cid,
				EncodingDetails:  pgtype.Text{String: "", Valid: true},
			}); err != nil {
				return nil, fmt.Errorf("Could not insert sound recording: %v", err)
			}

			if err := qtx.InsertManagementKey(ctx, db.InsertManagementKeyParams{
				TrackID: id,
				PubKey:  base64.StdEncoding.EncodeToString(ern.ReleaseHeader.Sender.PubKey),
			}); err != nil {
				return nil, fmt.Errorf("Could not insert management key: %v", err)
			}

			if ern.ReleaseHeader.SentOnBehalfOf != nil {
				if err := qtx.InsertManagementKey(ctx, db.InsertManagementKeyParams{
					TrackID: id,
					PubKey:  base64.StdEncoding.EncodeToString(ern.ReleaseHeader.SentOnBehalfOf.PubKey),
				}); err != nil {
					return nil, fmt.Errorf("Could not insert management key: %v", err)
				}
			}
		}
	}

	return tx, nil
}
