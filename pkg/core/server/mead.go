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
	// MEAD top level errors
	ErrMEADMessageValidation   = errors.New("MEAD message validation failed")
	ErrMEADMessageFinalization = errors.New("MEAD message finalization failed")

	// Create MEAD message validation errors
	ErrMEADAddressNotEmpty   = errors.New("MEAD address is not empty")
	ErrMEADFromAddressEmpty  = errors.New("MEAD from address is empty")
	ErrMEADToAddressNotEmpty = errors.New("MEAD to address is not empty")
	ErrMEADNonceNotOne       = errors.New("MEAD nonce is not one")

	// Update MEAD message validation errors
	ErrMEADAddressEmpty                     = errors.New("MEAD address is empty")
	ErrMEADToAddressEmpty                   = errors.New("MEAD to address is empty")
	ErrMEADAddressNotTo                     = errors.New("MEAD address is not the target of the message")
	ErrMEADNonceNotNext                     = errors.New("MEAD nonce is not the next nonce")
	ErrMEADResourceAndReleaseAddressesEmpty = errors.New("MEAD resource and release addresses are empty")
)

func (s *Server) finalizeMEAD(ctx context.Context, req *abcitypes.FinalizeBlockRequest, txhash string, tx *v1beta1.Transaction, messageIndex int64) error {
	if len(tx.Envelope.Messages) <= int(messageIndex) {
		return fmt.Errorf("message index out of range")
	}

	mead := tx.Envelope.Messages[messageIndex].GetMead()
	if mead == nil {
		return fmt.Errorf("tx: %s, message index: %d, MEAD message not found", txhash, messageIndex)
	}

	sender := tx.Envelope.Header.From

	// MEAD has no control type, always create a new MEAD
	if err := s.validateMEADNewMessage(ctx, mead); err != nil {
		return errors.Join(ErrMEADMessageValidation, err)
	}
	if err := s.finalizeMEADNewMessage(ctx, req, txhash, messageIndex, mead, sender); err != nil {
		return errors.Join(ErrMEADMessageFinalization, err)
	}
	return nil
}

/** MEAD New Message */

// Validate a MEAD message that's expected to be a NEW_MESSAGE, expects that the transaction header is valid
func (s *Server) validateMEADNewMessage(_ context.Context, mead *ddexv1beta1.MeadMessage) error {
	// TODO: add validation for conflicts and duplicates
	return nil
}

func (s *Server) finalizeMEADNewMessage(ctx context.Context, req *abcitypes.FinalizeBlockRequest, txhash string, messageIndex int64, mead *ddexv1beta1.MeadMessage, sender string) error {
	txhashBytes, err := common.HexToBytes(txhash)
	if err != nil {
		return fmt.Errorf("invalid txhash: %w", err)
	}
	// the MEAD address is the location of the message on the chain
	meadAddress := common.CreateAddress(txhashBytes, s.config.GenesisFile.ChainID, req.Height, messageIndex, "")

	rawMessage, err := proto.Marshal(mead)
	if err != nil {
		return fmt.Errorf("failed to marshal MEAD message: %w", err)
	}

	// Create acknowledgment for potential use in responses
	ack := &ddexv1beta1.MeadMessageAck{
		MeadAddress: meadAddress,
	}

	rawAcknowledgment, err := proto.Marshal(ack)
	if err != nil {
		return fmt.Errorf("failed to marshal MEAD acknowledgment: %w", err)
	}

	qtx := s.getDb()
	if err := qtx.InsertCoreMEAD(ctx, db.InsertCoreMEADParams{
		TxHash:            txhash,
		Index:             messageIndex,
		Address:           meadAddress,
		Sender:            sender,
		RawMessage:        rawMessage,
		RawAcknowledgment: rawAcknowledgment,
		BlockHeight:       req.Height,
	}); err != nil {
		return fmt.Errorf("failed to insert MEAD: %w", err)
	}

	return nil
}
