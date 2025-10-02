package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

func (s *Server) isValidFileUpload(ctx context.Context, tx *v1.SignedTransaction) error {
	// Check feature flag
	if !s.config.ProgrammableDistributionEnabled {
		return errors.New("programmable distribution is not enabled in this environment")
	}

	fu := tx.GetFileUpload()
	if fu == nil {
		return errors.New("file upload not present")
	}

	sig := fu.GetUploadSignature()
	if sig == "" {
		return errors.New("no signature in file upload")
	}

	uploader := fu.GetUploaderAddress()
	if uploader == "" {
		return errors.New("not uploader address")
	}

	cid := fu.GetCid()
	if cid == "" {
		return errors.New("no cid provided")
	}

	// Validate uploader signature for original CID
	sigData := &v1.UploadSignature{
		Cid: cid,
	}

	sigDataBytes, err := proto.Marshal(sigData)
	if err != nil {
		return fmt.Errorf("could not marshal sig data: %v", err)
	}

	_, address, err := common.EthRecover(sig, sigDataBytes)
	if err != nil {
		return fmt.Errorf("could not recover eth key: %v", err)
	}

	if address != uploader {
		return errors.New("uploader and signer mismatch")
	}

	// Validate validator signature for transcoded CID
	transcodedCid := fu.GetTranscodedCid()
	if transcodedCid == "" {
		return errors.New("no transcoded cid provided")
	}

	validatorSig := fu.GetValidatorSignature()
	if validatorSig == "" {
		return errors.New("no validator signature provided")
	}

	validatorAddress := fu.GetValidatorAddress()
	if validatorAddress == "" {
		return errors.New("no validator address provided")
	}

	// Verify validator signature for transcoded CID
	validatorSigData := &v1.UploadSignature{
		Cid: transcodedCid,
	}

	validatorSigDataBytes, err := proto.Marshal(validatorSigData)
	if err != nil {
		return fmt.Errorf("could not marshal validator sig data: %v", err)
	}

	_, recoveredValidatorAddress, err := common.EthRecover(validatorSig, validatorSigDataBytes)
	if err != nil {
		return fmt.Errorf("could not recover validator eth key: %v", err)
	}

	if !strings.EqualFold(recoveredValidatorAddress, validatorAddress) {
		return fmt.Errorf("validator address and signer mismatch: expected %s, got %s", validatorAddress, recoveredValidatorAddress)
	}

	// Check that validator is a registered mediorum node
	validators, err := s.db.GetAllRegisteredNodes(ctx)
	if err != nil {
		return fmt.Errorf("could not get validators: %v", err)
	}

	isValidator := false
	for _, v := range validators {
		if strings.EqualFold(v.EthAddress, validatorAddress) {
			isValidator = true
			break
		}
	}

	if !isValidator {
		return fmt.Errorf("validator %s is not a registered content/storage node", validatorAddress)
	}

	return nil
}

func (s *Server) finalizeFileUpload(ctx context.Context, tx *v1.SignedTransaction, txHash string, blockHeight int64) (proto.Message, error) {
	// Check feature flag
	if !s.config.ProgrammableDistributionEnabled {
		return nil, errors.New("programmable distribution is not enabled in this environment")
	}

	if err := s.isValidFileUpload(ctx, tx); err != nil {
		s.logger.Error("Invalid file upload:", zap.Error(err))
		return nil, err
	}

	fu := tx.GetFileUpload()
	if fu == nil {
		return nil, errors.New("finalizeFileUpload called with invalid tx")
	}

	qtx := s.getDb()

	err := qtx.InsertFileUpload(ctx, db.InsertFileUploadParams{
		UploaderAddress:    fu.UploaderAddress,
		Cid:                fu.Cid,
		TranscodedCid:      fu.TranscodedCid,
		Upid:               fu.UploadId,
		UploadSignature:    fu.UploadSignature,
		ValidatorAddress:   fu.ValidatorAddress,
		ValidatorSignature: fu.ValidatorSignature,
		TxHash:             txHash,
		BlockHeight:        blockHeight,
	})
	if err != nil {
		return nil, fmt.Errorf("could not store file upload tx: %v", err)
	}

	return nil, nil
}
