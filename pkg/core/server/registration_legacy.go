package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/AudiusProject/audiusd/pkg/core/common"
	"github.com/AudiusProject/audiusd/pkg/core/db"
	"github.com/AudiusProject/audiusd/pkg/core/gen/core_proto"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
)

// checks if the register node tx is valid
// calls ethereum mainnet and validates signature to confirm node should be a validator
func (s *Server) isValidLegacyRegisterNodeTx(_ *core_proto.SignedTransaction, blockHeight int64) error {
	// We only care if this attempted legacy registration is post-cutoff
	if blockHeight > s.config.LegacyRegistrationCutoff {
		return fmt.Errorf("Legacy registration is after cutoff.")
	}

	return nil
}

// persists the register node request should it pass validation
func (s *Server) finalizeLegacyRegisterNode(ctx context.Context, tx *core_proto.SignedTransaction, blockHeight int64) (*core_proto.ValidatorRegistrationLegacy, error) {
	if err := s.isValidLegacyRegisterNodeTx(tx, blockHeight); err != nil {
		return nil, fmt.Errorf("invalid register node tx: %v", err)
	}

	qtx := s.getDb()

	vr := tx.GetValidatorRegistration()
	sig := tx.GetSignature()
	txBytes, err := proto.Marshal(vr)
	if err != nil {
		return nil, fmt.Errorf("could not unmarshal tx bytes: %v", err)
	}

	pubKey, address, err := common.EthRecover(sig, txBytes)
	if err != nil {
		return nil, fmt.Errorf("could not recover signer: %v", err)
	}

	serializedPubKey, err := common.SerializePublicKey(pubKey)
	if err != nil {
		return nil, fmt.Errorf("could not serialize pubkey: %v", err)
	}

	registerNode := tx.GetValidatorRegistration()

	// Do not reinsert duplicate registrations
	if _, err = qtx.GetRegisteredNodeByEthAddress(ctx, address); errors.Is(err, pgx.ErrNoRows) {
		err = qtx.InsertRegisteredNode(ctx, db.InsertRegisteredNodeParams{
			PubKey:       serializedPubKey,
			EthAddress:   address,
			Endpoint:     registerNode.GetEndpoint(),
			CometAddress: registerNode.GetCometAddress(),
			CometPubKey:  base64.StdEncoding.EncodeToString(registerNode.GetPubKey()),
			EthBlock:     registerNode.GetEthBlock(),
			NodeType:     registerNode.GetNodeType(),
			SpID:         registerNode.GetSpId(),
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("error inserting registered node: %v", err)
		}
	}

	return vr, nil
}
