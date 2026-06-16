package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/httputil"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/opvalidation"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

func validateMediorumOperationShape(op *v1.MediorumOperation) error {
	if op == nil {
		return errors.New("mediorum operation not present")
	}
	return opvalidation.ValidateOperation(op.GetUlid(), op.GetHost(), op.GetAction(), op.GetTable(), op.GetData())
}

func (s *Server) isValidMediorumOperationTx(ctx context.Context, tx *v1.SignedTransaction) error {
	op := tx.GetMediorumOperation()
	if err := validateMediorumOperationShape(op); err != nil {
		return err
	}
	if tx.GetSignature() == "" {
		return errors.New("mediorum operation missing signature")
	}

	bodyBytes, err := proto.Marshal(op)
	if err != nil {
		return fmt.Errorf("marshal mediorum operation: %w", err)
	}
	_, signer, err := common.EthRecover(tx.GetSignature(), bodyBytes)
	if err != nil {
		return fmt.Errorf("recover mediorum operation signer: %w", err)
	}

	normalizedHost := httputil.RemoveTrailingSlash(strings.ToLower(op.GetHost()))
	nodes, err := s.db.GetAllRegisteredNodes(ctx)
	if err != nil {
		return fmt.Errorf("load registered nodes: %w", err)
	}
	for _, node := range nodes {
		if httputil.RemoveTrailingSlash(strings.ToLower(node.Endpoint)) == normalizedHost &&
			strings.EqualFold(node.EthAddress, signer) {
			return nil
		}
	}

	return fmt.Errorf("mediorum operation signer %s is not registered for host %s", signer, op.GetHost())
}

func (s *Server) finalizeMediorumOperation(ctx context.Context, tx *v1.SignedTransaction, txHash string) (proto.Message, error) {
	if err := s.isValidMediorumOperationTx(ctx, tx); err != nil {
		return nil, err
	}

	op := tx.GetMediorumOperation()
	if s.mediorumOperationApplier == nil {
		s.logger.Warn("mediorum operation applier not registered", zap.String("tx", txHash), zap.String("ulid", op.GetUlid()))
		return op, nil
	}
	if err := s.mediorumOperationApplier.ApplyMediorumOperation(ctx, op, txHash); err != nil {
		s.logger.Warn("failed to apply mediorum operation; syncer will retry from committed blocks",
			zap.String("tx", txHash),
			zap.String("ulid", op.GetUlid()),
			zap.Error(err))
	}
	return op, nil
}
