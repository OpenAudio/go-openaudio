package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/AudiusProject/audiusd/pkg/api/core/v1beta1"
	ddexv1beta1 "github.com/AudiusProject/audiusd/pkg/api/ddex/v1beta1"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/core/db"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	"google.golang.org/protobuf/proto"
)

var (
	// PIE top level errors
	ErrPIEMessageValidation   = errors.New("PIE message validation failed")
	ErrPIEMessageFinalization = errors.New("PIE message finalization failed")

	// Create PIE message validation errors
	ErrPIEAddressNotEmpty   = errors.New("PIE address is not empty")
	ErrPIEFromAddressEmpty  = errors.New("PIE from address is empty")
	ErrPIEToAddressNotEmpty = errors.New("PIE to address is not empty")
	ErrPIENonceNotOne       = errors.New("PIE nonce is not one")

	// Update PIE message validation errors
	ErrPIEAddressEmpty   = errors.New("PIE address is empty")
	ErrPIEToAddressEmpty = errors.New("PIE to address is empty")
	ErrPIEAddressNotTo   = errors.New("PIE address is not the target of the message")
	ErrPIENonceNotNext   = errors.New("PIE nonce is not the next nonce")
)

func (s *Server) finalizePIE(ctx context.Context, req *abcitypes.FinalizeBlockRequest, txhash string, tx *v1beta1.Transaction, messageIndex int64) error {
	if len(tx.Envelope.Messages) <= int(messageIndex) {
		return fmt.Errorf("message index out of range")
	}

	pie := tx.Envelope.Messages[messageIndex].GetPie()
	if pie == nil {
		return fmt.Errorf("tx: %s, message index: %d, PIE message not found", txhash, messageIndex)
	}

	sender := tx.Envelope.Header.From

	// PIE has no control type, always create a new PIE
	if err := s.validatePIENewMessage(ctx, pie); err != nil {
		return errors.Join(ErrPIEMessageValidation, err)
	}
	if err := s.finalizePIENewMessage(ctx, req, txhash, messageIndex, pie, sender); err != nil {
		return errors.Join(ErrPIEMessageFinalization, err)
	}
	return nil
}

/** PIE New Message */

// Validate a PIE message that's expected to be a NEW_MESSAGE, expects that the transaction header is valid
func (s *Server) validatePIENewMessage(_ context.Context, pie *ddexv1beta1.PieMessage) error {
	// TODO: add validation for conflicts and duplicates
	return nil
}

func (s *Server) finalizePIENewMessage(ctx context.Context, req *abcitypes.FinalizeBlockRequest, txhash string, messageIndex int64, pie *ddexv1beta1.PieMessage, sender string) error {
	txhashBytes, err := common.HexToBytes(txhash)
	if err != nil {
		return fmt.Errorf("invalid txhash: %w", err)
	}
	// the PIE address is the location of the message on the chain
	pieAddress := common.CreateAddress(txhashBytes, s.config.GenesisFile.ChainID, req.Height, messageIndex, "")

	rawMessage, err := proto.Marshal(pie)
	if err != nil {
		return fmt.Errorf("failed to marshal PIE message: %w", err)
	}

	// Create acknowledgment for potential use in responses
	ack := &ddexv1beta1.PieMessageAck{
		PieAddress: pieAddress,
	}

	rawAcknowledgment, err := proto.Marshal(ack)
	if err != nil {
		return fmt.Errorf("failed to marshal PIE acknowledgment: %w", err)
	}

	qtx := s.getDb()
	if err := qtx.InsertCorePIE(ctx, db.InsertCorePIEParams{
		TxHash:            txhash,
		Index:             messageIndex,
		Address:           pieAddress,
		Sender:            sender,
		RawMessage:        rawMessage,
		RawAcknowledgment: rawAcknowledgment,
		BlockHeight:       req.Height,
	}); err != nil {
		return fmt.Errorf("failed to insert PIE: %w", err)
	}

	return nil
}
