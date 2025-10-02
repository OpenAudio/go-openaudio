package server

import (
	"context"
	"errors"
	"fmt"

	corev1beta1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1beta1"
	ddexv1beta1 "github.com/OpenAudio/go-openaudio/pkg/api/ddex/v1beta1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	ethcommon "github.com/ethereum/go-ethereum/common"
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

func getERNOAPMessageSender(msg *ddexv1beta1.NewReleaseMessage) string {
	oapAddress := ""

	mh := msg.GetMessageHeader()
	if mh == nil {
		return oapAddress
	}

	ms := mh.GetMessageSender()
	if ms == nil {
		return oapAddress
	}

	pi := ms.GetPartyId()
	if pi == nil {
		return oapAddress
	}

	pis := pi.GetProprietaryIds()
	if len(pis) == 0 {
		return oapAddress
	}

	for _, pri := range pis {
		if pri.Namespace == common.OAPNamespace {
			if ethcommon.IsHexAddress(pri.Id) {
				oapAddress = pri.Id
			}
		}
	}

	return oapAddress
}

// Validate an ERN message that's expected to be a NEW_MESSAGE, expects that the transaction header is valid
func (s *Server) validateERNNewMessage(ctx context.Context, msg *ddexv1beta1.NewReleaseMessage) error {
	// Check feature flag
	if !s.config.ProgrammableDistributionEnabled {
		return errors.New("programmable distribution is not enabled in this environment")
	}

	resourceList := msg.GetResourceList()
	if resourceList == nil {
		return nil
	}

	if len(resourceList) == 0 {
		return nil
	}

	ernSender := getERNOAPMessageSender(msg)

	for _, resource := range resourceList {
		sr := resource.GetSoundRecording()
		if sr == nil {
			continue
		}

		sre := sr.GetSoundRecordingEdition()
		if sre == nil {
			continue
		}

		td := sre.GetTechnicalDetails()
		if td == nil {
			continue
		}

		df := td.GetDeliveryFile()
		if df == nil {
			continue
		}

		f := df.GetFile()
		if f == nil {
			continue
		}

		// in core this can be a CID (either original or transcoded)
		uri := f.Uri
		upload, err := s.db.GetCoreUpload(ctx, uri)
		if err != nil {
			return fmt.Errorf("file doesn't exist with cid %s: %v", uri, err)
		}

		uploader := upload.UploaderAddress
		if uploader != ernSender {
			return fmt.Errorf("sender %s doesn't match uploader %s for CID %s", ernSender, uploader, uri)
		}
	}

	return nil
}

func (s *Server) finalizeERNNewMessage(ctx context.Context, req *abcitypes.FinalizeBlockRequest, txhash string, messageIndex int64, ern *ddexv1beta1.NewReleaseMessage, sender string) error {
	// Convert txhash to bytes for address generation
	txhashBytes, err := common.HexToBytes(txhash)
	if err != nil {
		return fmt.Errorf("failed to decode txhash: %w", err)
	}

	chainID := s.config.GenesisFile.ChainID

	// Generate ERN address using message ID
	messageID := ""
	if ern.MessageHeader != nil && ern.MessageHeader.MessageId != "" {
		messageID = ern.MessageHeader.MessageId
	} else {
		// Fallback to txhash + index if no message ID
		messageID = fmt.Sprintf("%s:%d", txhash, messageIndex)
	}
	ernAddress := common.CreateERNAddress(txhashBytes, chainID, req.Height, messageIndex, messageID)

	// Collect all addresses using deterministic references
	partyAddresses := make([]string, len(ern.PartyList))
	for i, party := range ern.PartyList {
		partyAddresses[i] = common.CreatePartyAddress(txhashBytes, chainID, req.Height, messageIndex, party.PartyReference)
	}

	resourceAddresses := make([]string, len(ern.ResourceList))
	for i, resource := range ern.ResourceList {
		ref := ""
		if sr := resource.GetSoundRecording(); sr != nil {
			ref = sr.ResourceReference
		} else if img := resource.GetImage(); img != nil {
			ref = img.ResourceReference
		}
		resourceAddresses[i] = common.CreateResourceAddress(txhashBytes, chainID, req.Height, messageIndex, ref)
	}

	releaseAddresses := make([]string, len(ern.ReleaseList))
	for i, release := range ern.ReleaseList {
		ref := ""
		if mr := release.GetMainRelease(); mr != nil {
			ref = mr.ReleaseReference
		} else if tr := release.GetTrackRelease(); tr != nil {
			ref = tr.ReleaseReference
		}
		releaseAddresses[i] = common.CreateReleaseAddress(txhashBytes, chainID, req.Height, messageIndex, ref)
	}

	dealAddresses := make([]string, len(ern.DealList))
	for i, deal := range ern.DealList {
		dealAddresses[i] = common.CreateDealAddress(txhashBytes, chainID, req.Height, messageIndex, deal.String())
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
		RawMessage:         rawMessage,
		RawAcknowledgment:  rawAcknowledgment,
		BlockHeight:        req.Height,
	}); err != nil {
		return fmt.Errorf("failed to insert ERN: %w", err)
	}

	// Insert normalized entity records
	for i, partyAddress := range partyAddresses {
		if err := qtx.InsertCoreParty(ctx, db.InsertCorePartyParams{
			Address:     partyAddress,
			ErnAddress:  ernAddress,
			EntityType:  "party",
			EntityIndex: int32(i + 1), // 1-indexed to match PostgreSQL array conventions
			TxHash:      txhash,
			BlockHeight: req.Height,
		}); err != nil {
			return fmt.Errorf("failed to insert party %d: %w", i, err)
		}
	}

	for i, resourceAddress := range resourceAddresses {
		if err := qtx.InsertCoreResource(ctx, db.InsertCoreResourceParams{
			Address:     resourceAddress,
			ErnAddress:  ernAddress,
			EntityType:  "resource",
			EntityIndex: int32(i + 1), // 1-indexed to match PostgreSQL array conventions
			TxHash:      txhash,
			BlockHeight: req.Height,
		}); err != nil {
			return fmt.Errorf("failed to insert resource %d: %w", i, err)
		}
	}

	for i, releaseAddress := range releaseAddresses {
		if err := qtx.InsertCoreRelease(ctx, db.InsertCoreReleaseParams{
			Address:     releaseAddress,
			ErnAddress:  ernAddress,
			EntityType:  "release",
			EntityIndex: int32(i + 1), // 1-indexed to match PostgreSQL array conventions
			TxHash:      txhash,
			BlockHeight: req.Height,
		}); err != nil {
			return fmt.Errorf("failed to insert release %d: %w", i, err)
		}
	}

	for i, dealAddress := range dealAddresses {
		if err := qtx.InsertCoreDeal(ctx, db.InsertCoreDealParams{
			Address:     dealAddress,
			ErnAddress:  ernAddress,
			EntityType:  "deal",
			EntityIndex: int32(i + 1), // 1-indexed to match PostgreSQL array conventions
			TxHash:      txhash,
			BlockHeight: req.Height,
		}); err != nil {
			return fmt.Errorf("failed to insert deal %d: %w", i, err)
		}
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
