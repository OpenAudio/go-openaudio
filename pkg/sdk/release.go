package sdk

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	adx "github.com/AudiusProject/audiusd/pkg/api/ddex/v1beta1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

type ReleaseResult struct {
	TxHash  string
	TrackID string
}

func (s *AudiusdSDK) ReleaseTrack(ctx context.Context, cid, title, genre string) (*ReleaseResult, error) {
	if s.privKey == nil {
		return nil, errors.New("no private key set, cannot release track")
	}
	trackId := uuid.NewString()
	ern := &adx.NewReleaseMessage{
		ReleaseHeader: &adx.ReleaseHeader{
			Sender: &adx.Party{
				PartyId: "audius_sdk",
				Address: s.Address(),
			},
		},
		ResourceList: []*adx.Resource{
			{
				ResourceReference: "AT1",
				Resource: &adx.Resource_SoundRecording{
					SoundRecording: &adx.SoundRecording{
						Cid: cid,
						Id: &adx.SoundRecordingId{
							Isrc: uuid.NewString(),
						},
					},
				},
			},
		},
		ReleaseList: []*adx.Release{
			{
				Release: &adx.Release_TrackRelease{
					TrackRelease: &adx.TrackRelease{
						ReleaseId: &adx.ReleaseId{
							Isrc: trackId,
						},
						ReleaseResourceReference: "AT1",
						Title:                    title,
						Genre:                    genre,
					},
				},
			},
		},
	}

	ernBytes, err := proto.Marshal(ern)
	if err != nil {
		return nil, fmt.Errorf("failure to marshal ern: %v", err)
	}

	sig, err := s.Sign(ernBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to sign message: %w", err)
	}

	tx := &v1.SignedTransaction{
		Signature: sig,
		RequestId: uuid.NewString(),
		Transaction: &v1.SignedTransaction_Release{
			Release: ern,
		},
	}

	res, err := s.Core.SendTransaction(ctx, connect.NewRequest(&v1.SendTransactionRequest{
		Transaction: tx,
	}))
	if err != nil {
		return nil, fmt.Errorf("ern failed: %w", err)
	}

	return &ReleaseResult{TrackID: trackId, TxHash: res.Msg.Transaction.Hash}, nil
}
