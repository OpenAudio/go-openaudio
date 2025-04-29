package server

import (
	"context"
	"errors"
	"fmt"

	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"google.golang.org/protobuf/proto"
)

func (s *Server) validateManageEntity(_ context.Context, stx *v1.SignedTransaction) (proto.Message, error) {
	manageEntity := stx.GetManageEntity()
	if manageEntity == nil {
		return nil, errors.New("not manage entity")
	}
	return manageEntity, nil
}

func (s *Server) finalizeManageEntity(ctx context.Context, stx *v1.SignedTransaction) (proto.Message, error) {
	tx, err := s.validateManageEntity(ctx, stx)
	if err != nil {
		return nil, fmt.Errorf("invalid manage entity: %v", err)
	}

	manageEntity := tx.(*v1.ManageEntityLegacy)

	return manageEntity, nil
}
