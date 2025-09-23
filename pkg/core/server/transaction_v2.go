package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/AudiusProject/audiusd/pkg/api/core/v1beta1"
	ddexv1beta1 "github.com/AudiusProject/audiusd/pkg/api/ddex/v1beta1"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

var (
	ErrV2TransactionExpired        = errors.New("transaction expired")
	ErrV2TransactionInvalidChainID = errors.New("invalid chain id")
)

func (s *Server) validateV2Transaction(ctx context.Context, currentHeight int64, tx *v1beta1.Transaction) error {
	header := tx.Envelope.Header
	if header.ChainId != s.config.GenesisFile.ChainID {
		return ErrV2TransactionInvalidChainID
	}

	if header.Expiration < currentHeight {
		return ErrV2TransactionExpired
	}

	// TODO: check signature

	to := tx.Envelope.Header.To
	from := tx.Envelope.Header.From

	// use errgroup to validate all messages
	eg := errgroup.Group{}
	for _, msg := range tx.Envelope.Messages {
		eg.Go(func() error {
			switch msg.Message.(type) {
			case *v1beta1.Message_Ern:
				switch msg.GetErn().MessageHeader.MessageControlType {
				case ddexv1beta1.MessageControlType_MESSAGE_CONTROL_TYPE_NEW_MESSAGE.Enum():
					return s.validateERNNewMessage(ctx, msg.GetErn())
				case ddexv1beta1.MessageControlType_MESSAGE_CONTROL_TYPE_UPDATED_MESSAGE.Enum():
					return s.validateERNUpdateMessage(ctx, to, from, msg.GetErn())
				case ddexv1beta1.MessageControlType_MESSAGE_CONTROL_TYPE_TAKEDOWN_MESSAGE.Enum():
					return s.validateERNTakedownMessage(ctx, msg.GetErn())
				}
			case *v1beta1.Message_Mead:
				return s.validateMEADNewMessage(ctx, msg.GetMead())
			case *v1beta1.Message_Pie:
				return s.validatePIENewMessage(ctx, msg.GetPie())
			}
			return nil
		})
	}
	return eg.Wait()
}

func (s *Server) finalizeV2Transaction(ctx context.Context, req *abcitypes.FinalizeBlockRequest, tx *v1beta1.Transaction, txhash string) error {
	header := tx.Envelope.Header
	if header.ChainId != s.config.GenesisFile.ChainID {
		return fmt.Errorf("invalid chain id: %s", header.ChainId)
	}

	if header.Expiration < req.Height {
		return fmt.Errorf("transaction expired")
	}

	// Use pre-calculated transaction hash for consistency

	s.logger.Debug("finalizing v2 transaction", zap.String("tx", txhash), zap.Int("messages", len(tx.Envelope.Messages)))

	for i, msg := range tx.Envelope.Messages {
		var err error
		switch msg.Message.(type) {
		case *v1beta1.Message_Ern:
			err = s.finalizeERN(ctx, req, txhash, tx, int64(i))
			if err != nil {
				return fmt.Errorf("failed to finalize ERN message: %w", err)
			}
		case *v1beta1.Message_Mead:
			err = s.finalizeMEAD(ctx, req, txhash, tx, int64(i))
			if err != nil {
				return fmt.Errorf("failed to finalize MEAD message: %w", err)
			}
		case *v1beta1.Message_Pie:
			err = s.finalizePIE(ctx, req, txhash, tx, int64(i))
			if err != nil {
				return fmt.Errorf("failed to finalize PIE message: %w", err)
			}
		}
	}
	return nil
}
