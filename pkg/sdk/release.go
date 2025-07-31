package sdk

import (
	"context"
	"errors"
)

type ReleaseResult struct {
	TxHash  string
	TrackID string
}

func (s *AudiusdSDK) ReleaseTrack(ctx context.Context, cid, title, genre string) (*ReleaseResult, error) {
	if s.privKey == nil {
		return nil, errors.New("no private key set, cannot release track")
	}

	return nil, errors.New("not implemented")
}
