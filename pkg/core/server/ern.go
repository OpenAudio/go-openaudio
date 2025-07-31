package server

import (
	"context"
	"errors"
	"fmt"

	corev1beta1 "github.com/AudiusProject/audiusd/pkg/api/core/v1beta1"
	ddexv1beta1 "github.com/AudiusProject/audiusd/pkg/api/ddex/v1beta1"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/core/db"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	"google.golang.org/protobuf/proto"
)

var (
	// ERN top level errors
	ErrERNMessageValidation   = errors.New("ERN message validation failed")
	ErrERNMessageFinalization = errors.New("ERN message finalization failed")

	// Create ERN message validation errors
	ErrERNAddressNotEmpty   = errors.New("ERN address is not empty")
	ErrERNFromAddressEmpty  = errors.New("ERN from address is empty")
	ErrERNToAddressNotEmpty = errors.New("ERN to address is not empty")
	ErrERNNonceNotOne       = errors.New("ERN nonce is not one")

	// Update ERN message validation errors
	ErrERNAddressEmpty   = errors.New("ERN address is empty")
	ErrERNToAddressEmpty = errors.New("ERN to address is empty")
	ErrERNAddressNotTo   = errors.New("ERN address is not the target of the message")
	ErrERNNonceNotNext   = errors.New("ERN nonce is not the next nonce")
)

func (s *Server) finalizeERN(ctx context.Context, req *abcitypes.FinalizeBlockRequest, txhash string, tx *corev1beta1.Transaction, messageIndex int64) error {
	if len(tx.Envelope.Messages) <= int(messageIndex) {
		return fmt.Errorf("message index out of range")
	}

	sender := tx.Envelope.Header.From
	receiver := tx.Envelope.Header.To

	ern := tx.Envelope.Messages[messageIndex].GetErn()
	if ern == nil {
		return fmt.Errorf("tx: %s, message index: %d, ERN message not found", txhash, messageIndex)
	}

	if ern.MessageHeader.MessageControlType == nil {
		return fmt.Errorf("tx: %s, message index: %d, ERN message control type is nil", txhash, messageIndex)
	}

	switch *ern.MessageHeader.MessageControlType {
	case ddexv1beta1.MessageControlType_MESSAGE_CONTROL_TYPE_NEW_MESSAGE, ddexv1beta1.MessageControlType_MESSAGE_CONTROL_TYPE_TEST_MESSAGE:
		if err := s.validateERNNewMessage(ctx, ern); err != nil {
			return errors.Join(ErrERNMessageValidation, err)
		}
		if err := s.finalizeERNNewMessage(ctx, req, txhash, messageIndex, ern, sender); err != nil {
			return errors.Join(ErrERNMessageFinalization, err)
		}
		return nil

	case ddexv1beta1.MessageControlType_MESSAGE_CONTROL_TYPE_UPDATED_MESSAGE:
		if err := s.validateERNUpdateMessage(ctx, receiver, sender, ern); err != nil {
			return errors.Join(ErrERNMessageValidation, err)
		}
		if err := s.finalizeERNUpdateMessage(ctx, req, txhash, messageIndex, receiver, sender, ern); err != nil {
			return errors.Join(ErrERNMessageFinalization, err)
		}
		return nil
	case ddexv1beta1.MessageControlType_MESSAGE_CONTROL_TYPE_TAKEDOWN_MESSAGE:
		if err := s.validateERNTakedownMessage(ctx, ern); err != nil {
			return errors.Join(ErrERNMessageValidation, err)
		}
		if err := s.finalizeERNTakedownMessage(ctx, req, txhash, messageIndex, ern); err != nil {
			return errors.Join(ErrERNMessageFinalization, err)
		}
		return nil
	case ddexv1beta1.MessageControlType_MESSAGE_CONTROL_TYPE_UNSPECIFIED:
		return fmt.Errorf("tx: %s, message index: %d, ERN message control type is unspecified", txhash, messageIndex)
	default:
		return fmt.Errorf("tx: %s, message index: %d, unsupported ERN message control type: %s", txhash, messageIndex, ern.MessageHeader.MessageControlType)
	}
}

/** ERN New Message */

// Validate an ERN message that's expected to be a NEW_MESSAGE, expects that the transaction header is valid
func (s *Server) validateERNNewMessage(_ context.Context, _ *ddexv1beta1.NewReleaseMessage) error {
	// TODO: add ERN level validation for conflicts and duplicates
	return nil
}

func (s *Server) finalizeERNNewMessage(ctx context.Context, req *abcitypes.FinalizeBlockRequest, txhash string, messageIndex int64, ern *ddexv1beta1.NewReleaseMessage, sender string) error {
	// TODO: use a better nonce
	nonce := txhash
	// the ERN address is the location of the message on the chain
	ernAddress := common.CreateAddress(ern, s.config.GenesisFile.ChainID, req.Height, txhash)

	// Collect all addresses, all underlying objects use the same source ERN nonce
	partyAddresses := make([]string, len(ern.PartyList))
	for i, party := range ern.PartyList {
		partyAddresses[i] = common.CreateAddress(party, s.config.GenesisFile.ChainID, req.Height, nonce)
	}

	resourceAddresses := make([]string, len(ern.ResourceList))
	for i, resource := range ern.ResourceList {
		resourceAddresses[i] = common.CreateAddress(resource, s.config.GenesisFile.ChainID, req.Height, nonce)
	}

	releaseAddresses := make([]string, len(ern.ReleaseList))
	for i, release := range ern.ReleaseList {
		releaseAddresses[i] = common.CreateAddress(release, s.config.GenesisFile.ChainID, req.Height, nonce)
	}

	dealAddresses := make([]string, len(ern.DealList))
	for i, deal := range ern.DealList {
		dealAddresses[i] = common.CreateAddress(deal, s.config.GenesisFile.ChainID, req.Height, nonce)
	}

	rawMessage, err := proto.Marshal(ern)
	if err != nil {
		return fmt.Errorf("failed to marshal ERN message: %w", err)
	}

	ack := &ddexv1beta1.NewReleaseMessageAck{
		ErnAddress:        ernAddress,
		PartyAddresses:    partyAddresses,
		ResourceAddresses: resourceAddresses,
		ReleaseAddresses:  releaseAddresses,
		DealAddresses:     dealAddresses,
	}

	rawAcknowledgment, err := proto.Marshal(ack)
	if err != nil {
		return fmt.Errorf("failed to marshal ERN acknowledgment: %w", err)
	}

	qtx := s.getDb()
	if err := qtx.InsertCoreERN(ctx, db.InsertCoreERNParams{
		TxHash:             txhash,
		Index:              messageIndex,
		Address:            ernAddress,
		Sender:             sender,
		MessageControlType: int16(*ern.MessageHeader.MessageControlType),
		PartyAddresses:     partyAddresses,
		ResourceAddresses:  resourceAddresses,
		ReleaseAddresses:   releaseAddresses,
		DealAddresses:      dealAddresses,
		RawMessage:         rawMessage,
		RawAcknowledgment:  rawAcknowledgment,
		BlockHeight:        req.Height,
	}); err != nil {
		return fmt.Errorf("failed to insert ERN: %w", err)
	}

	return nil
}

/** ERN Update Message */

// TODO: profile this function
func (s *Server) validateERNUpdateMessage(ctx context.Context, to string, from string, ern *ddexv1beta1.NewReleaseMessage) error {
	// TODO: get ERN from the DB
	// TODO: validate to address exists in the DB and is an ERN
	// TODO: compare initial sender and from address
	return nil
}

func (s *Server) finalizeERNUpdateMessage(ctx context.Context, req *abcitypes.FinalizeBlockRequest, txhash string, messageIndex int64, to string, from string, ern *ddexv1beta1.NewReleaseMessage) error {
	if err := s.validateERNUpdateMessage(ctx, to, from, ern); err != nil {
		return errors.Join(ErrERNMessageValidation, err)
	}
	return nil
}

/** ERN Takedown Message */

func (s *Server) validateERNTakedownMessage(_ context.Context, _ *ddexv1beta1.NewReleaseMessage) error {
	return nil
}

func (s *Server) finalizeERNTakedownMessage(ctx context.Context, _ *abcitypes.FinalizeBlockRequest, _ string, _ int64, ern *ddexv1beta1.NewReleaseMessage) error {
	if err := s.validateERNTakedownMessage(ctx, ern); err != nil {
		return errors.Join(ErrERNMessageValidation, err)
	}
	return nil
}
