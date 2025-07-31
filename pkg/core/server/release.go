package server

import (
	"context"

	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
)

func (s *Server) isValidReleaseTx(ctx context.Context, tx *v1.SignedTransaction) error {
	return nil
}

func (s *Server) finalizeRelease(ctx context.Context, tx *v1.SignedTransaction, txHash string) (*v1.SignedTransaction, error) {
	return nil, nil
}
