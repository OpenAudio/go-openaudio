package server

import (
	"context"
	"errors"
	"fmt"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
)

func (s *Server) isValidPlayTransaction(_ context.Context, _ *v1.SignedTransaction) error {
	return nil
}

func (s *Server) finalizePlayTransaction(ctx context.Context, stx *v1.SignedTransaction) (*v1.TrackPlays, error) {
	if err := s.isValidPlayTransaction(ctx, stx); err != nil {
		return nil, fmt.Errorf("invalid play tx: %v", err)
	}

	tx := stx.GetPlays()
	if tx == nil {
		return nil, errors.New("invalid play tx")
	}

	return tx, nil
}
