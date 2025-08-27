package server

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"time"

	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/core/db"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	cometcrypto "github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
)

const maxRegistrationAttestationValidity = 24 * 60 * 60   // Registration attestations are only valid for approx 24 hours
const maxDeregistrationAttestationValidity = 24 * 60 * 60 // Deregistration attestations are only valid for approx 24 hours

func (s *Server) isValidRegisterNodeAttestation(ctx context.Context, tx *v1.SignedTransaction, signers []string, blockHeight int64) error {
	vr := tx.GetAttestation().GetValidatorRegistration()
	if vr == nil {
		return fmt.Errorf("unknown tx fell into isValidRegisterNodeAttestation: %v", tx)
	}

	// validate address from tx signature
	sig := tx.GetSignature()
	if sig == "" {
		return fmt.Errorf("no signature provided for registration tx: %v", tx)
	}
	attBytes, err := proto.Marshal(tx.GetAttestation())
	if err != nil {
		return fmt.Errorf("could not marshal registration tx: %v", err)
	}
	_, address, err := common.EthRecover(tx.GetSignature(), attBytes)
	if err != nil {
		return fmt.Errorf("could not recover msg sig: %v", err)
	}
	if address != vr.GetDelegateWallet() {
		return fmt.Errorf("signature address '%s' does not match ethereum registration '%s'", address, vr.GetDelegateWallet())
	}

	// validate voting power
	if vr.GetPower() != int64(s.config.ValidatorVotingPower) {
		return fmt.Errorf("invalid voting power '%d'", vr.GetPower())
	}

	// validate pub key
	if len(vr.GetPubKey()) == 0 {
		return fmt.Errorf("public Key missing from %s registration tx", vr.GetEndpoint())
	}
	vrPubKey := ed25519.PubKey(vr.GetPubKey())
	if vrPubKey.Address().String() != vr.GetCometAddress() {
		return fmt.Errorf("address does not match public key: %s %s", vrPubKey.Address(), vr.GetCometAddress())
	}

	// ensure comet address is not already taken
	if _, err := s.db.GetRegisteredNodeByCometAddress(context.Background(), vr.GetCometAddress()); !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("address '%s' is already registered on comet, node %s attempted to acquire it", vr.GetCometAddress(), vr.GetEndpoint())
	}

	// validate age of request
	if vr.Deadline < blockHeight || vr.Deadline > blockHeight+maxRegistrationAttestationValidity {
		return fmt.Errorf("registration request for '%s' with deadline %d is too new/old (current height is %d)", vr.GetEndpoint(), vr.Deadline, blockHeight)
	}

	// validate signers
	keyBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(keyBytes, uint64(vr.GetEthBlock()))
	enough, err := s.attestationHasEnoughSigners(ctx, signers, keyBytes, s.config.AttRegistrationRSize, s.config.AttRegistrationMin, "")
	if err != nil {
		return fmt.Errorf("error checking attestors against validators: %v", err)
	} else if !enough {
		return fmt.Errorf("not enough attestations provided to register validator at '%s'", vr.GetEndpoint())
	}

	return nil
}

func (s *Server) finalizeRegisterNodeAttestation(ctx context.Context, tx *v1.SignedTransaction, blockHeight int64) error {
	qtx := s.getDb()
	vr := tx.GetAttestation().GetValidatorRegistration()

	txBytes, err := proto.Marshal(tx)
	if err != nil {
		return fmt.Errorf("could not unmarshal tx bytes: %v", err)
	}
	pubKey, _, err := common.EthRecover(tx.GetSignature(), txBytes)
	if err != nil {
		return fmt.Errorf("could not recover signer: %v", err)
	}

	serializedPubKey := common.SerializePublicKeyHex(pubKey)

	// Do not reinsert duplicate registrations
	if _, err = qtx.GetRegisteredNodeByEthAddress(ctx, vr.GetDelegateWallet()); errors.Is(err, pgx.ErrNoRows) {
		err = qtx.InsertRegisteredNode(ctx, db.InsertRegisteredNodeParams{
			PubKey:       serializedPubKey,
			EthAddress:   vr.GetDelegateWallet(),
			Endpoint:     vr.GetEndpoint(),
			CometAddress: vr.GetCometAddress(),
			CometPubKey:  base64.StdEncoding.EncodeToString(vr.GetPubKey()),
			EthBlock:     strconv.FormatInt(vr.GetEthBlock(), 10),
			NodeType:     vr.GetNodeType(),
			SpID:         vr.GetSpId(),
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("error inserting registered node: %v", err)
		}
	}
	return nil
}

func (s *Server) isValidDeregisterNodeAttestation(ctx context.Context, tx *v1.SignedTransaction, signers []string, blockHeight int64) error {
	att := tx.GetAttestation()
	if att == nil {
		return fmt.Errorf("unknown attestation fell into isValidDeregisterNodeAttestation: %v", tx)
	}
	dereg := att.GetValidatorDeregistration()
	if dereg == nil {
		return fmt.Errorf("unknown attestation fell into isValidDeregisterNodeAttestation: %v", tx)
	}

	addr := dereg.GetCometAddress()

	if len(dereg.GetPubKey()) == 0 {
		return fmt.Errorf("public Key missing from deregistration attestation: %v", tx)
	}
	vdPubKey := ed25519.PubKey(dereg.GetPubKey())
	if len(vdPubKey) != ed25519.PubKeySize {
		return fmt.Errorf("incorrect pubkey size %d, must by %d", len(vdPubKey), ed25519.PubKeySize)
	}
	if vdPubKey.Address().String() != addr {
		return fmt.Errorf("address does not match public key: %s %s", vdPubKey.Address(), addr)
	}

	// validate age of request
	if dereg.Deadline < blockHeight || dereg.Deadline > blockHeight+maxDeregistrationAttestationValidity {
		return fmt.Errorf("fegistration request for '%s' with deadline %d is too new/old (current height is %d)", addr, dereg.Deadline, blockHeight)
	}

	node, err := s.db.GetRegisteredNodeByCometAddress(ctx, addr)
	if err != nil {
		return fmt.Errorf("error getting eth address from node deregistration attestation: %v", err)
	}

	// validate signers
	enough, err := s.attestationHasEnoughSigners(ctx, signers, vdPubKey.Bytes(), s.config.AttDeregistrationRSize, s.config.AttDeregistrationMin, node.EthAddress)
	if err != nil {
		return fmt.Errorf("error checking attestors against validators: %v", err)
	} else if !enough {
		return fmt.Errorf("not enough attestations provided to deregister validator '%s'", addr)
	}

	return nil
}

func (s *Server) finalizeDeregisterValidatorAttestation(ctx context.Context, tx *v1.SignedTransaction) error {
	dereg := tx.GetAttestation().GetValidatorDeregistration()
	if dereg == nil {
		return fmt.Errorf("unknown attestation fell into isValidDeregisterNodeAttestation: %v", tx)
	}
	qtx := s.getDb()
	err := qtx.DeleteRegisteredNode(ctx, dereg.GetCometAddress())
	if err != nil {
		return fmt.Errorf("error deleting registered node: %v", err)
	}

	return nil
}

func (s *Server) isValidDeregisterMisbehavingNodeTx(tx *v1.SignedTransaction, misbehavior []abcitypes.Misbehavior) error {
	sig := tx.GetSignature()
	if sig == "" {
		return fmt.Errorf("no signature provided for deregistration tx: %v", tx)
	}

	vd := tx.GetValidatorDeregistration()
	if vd == nil {
		return fmt.Errorf("unknown tx fell into isValidDeregisterMisbehavingNodeTx: %v", tx)
	}

	addr := vd.GetCometAddress()

	_, err := s.db.GetRegisteredNodeByCometAddress(context.Background(), addr)
	if err != nil {
		return fmt.Errorf("not able to find registered node: %v", err)
	}

	if len(vd.GetPubKey()) == 0 {
		return fmt.Errorf("public Key missing from deregistration tx: %v", tx)
	}
	vdPubKey := ed25519.PubKey(vd.GetPubKey())
	if vdPubKey.Address().String() != addr {
		return fmt.Errorf("address does not match public key: %s %s", vdPubKey.Address(), addr)
	}

	for _, mb := range misbehavior {
		validator := mb.GetValidator()
		if addr == cometcrypto.Address(validator.GetAddress()).String() {
			return nil
		}
	}

	return fmt.Errorf("no misbehavior found matching deregistration tx: %v", tx)
}

func (s *Server) finalizeDeregisterMisbehavingNode(ctx context.Context, tx *v1.SignedTransaction, misbehavior []abcitypes.Misbehavior) (*v1.ValidatorMisbehaviorDeregistration, error) {
	if err := s.isValidDeregisterMisbehavingNodeTx(tx, misbehavior); err != nil {
		return nil, fmt.Errorf("invalid deregister node tx: %v", err)
	}

	vd := tx.GetValidatorDeregistration()
	qtx := s.getDb()
	err := qtx.DeleteRegisteredNode(ctx, vd.GetCometAddress())
	if err != nil {
		return nil, fmt.Errorf("error deleting registered node: %v", err)
	}

	return vd, nil
}

func (s *Server) createDeregisterTransaction(address types.Address) ([]byte, error) {
	node, err := s.db.GetRegisteredNodeByCometAddress(context.Background(), address.String())
	if err != nil {
		return []byte{}, fmt.Errorf("not able to find registered node with address '%s': %v", address.String(), err)
	}
	pubkeyEnc, err := base64.StdEncoding.DecodeString(node.CometPubKey)
	if err != nil {
		return []byte{}, fmt.Errorf("could not decode public key '%s' as base64 encoded string: %v", node.CometPubKey, err)
	}
	deregistrationTx := &v1.ValidatorMisbehaviorDeregistration{
		PubKey:       pubkeyEnc,
		CometAddress: address.String(),
	}

	txBytes, err := proto.Marshal(deregistrationTx)
	if err != nil {
		return []byte{}, fmt.Errorf("failure to marshal deregister tx: %v", err)
	}

	sig, err := common.EthSign(s.config.EthereumKey, txBytes)
	if err != nil {
		return []byte{}, fmt.Errorf("could not sign deregister tx: %v", err)
	}

	tx := v1.SignedTransaction{
		Signature: sig,
		RequestId: uuid.NewString(),
		Transaction: &v1.SignedTransaction_ValidatorDeregistration{
			ValidatorDeregistration: deregistrationTx,
		},
	}

	signedTxBytes, err := proto.Marshal(&tx)
	if err != nil {
		return []byte{}, err
	}
	return signedTxBytes, nil
}

func (s *Server) appendDeregistrationToValidatorHistory(ctx context.Context, dereg *v1.ValidatorDeregistration, timestamp time.Time, block int64) error {
	// get validator metadata from db before deregistration state is committed
	validator, err := s.db.GetRegisteredNodeByCometAddress(ctx, dereg.GetCometAddress())
	if err != nil {
		return fmt.Errorf("could not get metadata for node being deregistered: %v", err)
	}

	// convert spid
	spId64, err := strconv.ParseInt(validator.SpID, 10, 64)
	if err != nil {
		return err
	}

	qtx := s.getDb()
	if err := qtx.AppendValidatorHistory(ctx, db.AppendValidatorHistoryParams{
		Endpoint:     validator.Endpoint,
		EthAddress:   validator.EthAddress,
		CometAddress: dereg.CometAddress,
		SpID:         spId64,
		ServiceType:  validator.NodeType,
		EventType:    db.ValidatorEventDeregistered,
		EventTime:    qtx.ToPgxTimestamp(timestamp),
		EventBlock:   block,
	}); err != nil {
		return err
	}

	return nil
}

func (s *Server) appendRegistrationToValidatorHistory(ctx context.Context, reg *v1.ValidatorRegistration, timestamp time.Time, block int64) error {
	// convert spid
	spId64, err := strconv.ParseInt(reg.SpId, 10, 64)
	if err != nil {
		return err
	}

	qtx := s.getDb()
	if err := qtx.AppendValidatorHistory(ctx, db.AppendValidatorHistoryParams{
		Endpoint:     reg.Endpoint,
		EthAddress:   reg.DelegateWallet,
		CometAddress: reg.CometAddress,
		SpID:         spId64,
		ServiceType:  reg.NodeType,
		EventType:    db.ValidatorEventRegistered,
		EventTime:    qtx.ToPgxTimestamp(timestamp),
		EventBlock:   block,
	}); err != nil {
		return err
	}
	return nil
}
