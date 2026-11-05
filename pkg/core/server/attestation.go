package server

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

func (s *Server) isValidAttestation(ctx context.Context, tx *v1.SignedTransaction, blockHeight int64) error {
	att := tx.GetAttestation()
	if att == nil {
		return errors.New("empty attestation tx")
	}

	var body proto.Message
	var rendezvousSize int
	switch t := att.Body.(type) {
	case *v1.Attestation_ValidatorRegistration:
		body = t.ValidatorRegistration
		rendezvousSize = s.config.AttRegistrationRSize
	case *v1.Attestation_ValidatorDeregistration:
		body = t.ValidatorDeregistration
		rendezvousSize = s.config.AttDeregistrationRSize
	default:
		return fmt.Errorf("unhandled attestation body: %T", t)
	}
	// Recover at most one signature per possible attestor. This bounds the
	// work an untrusted submission can trigger before its signers are known.
	if s.config.Upgrades.RulesetAt(blockHeight).AttestationDomainEnforced && len(att.Signatures) > rendezvousSize {
		return errors.New("more signatures than eligible attestor slots")
	}
	bodyBytes, err := s.validatorAttestationBytes(body, blockHeight)
	if err != nil {
		return fmt.Errorf("could not marshal attestation body into bytes: %v", err)
	}
	signerAddrs, err := recoverSigners(att.Signatures, bodyBytes)
	if err != nil {
		return fmt.Errorf("could not recover signers: %v", err)
	}

	switch t := att.Body.(type) {
	case *v1.Attestation_ValidatorRegistration:
		return s.isValidRegisterNodeAttestation(ctx, tx, signerAddrs, blockHeight)
	case *v1.Attestation_ValidatorDeregistration:
		return s.isValidDeregisterNodeAttestation(ctx, tx, signerAddrs, blockHeight)
	default:
		return fmt.Errorf("unhandled attestation: %v %T", tx, t)
	}
}

func (s *Server) finalizeAttestation(ctx context.Context, tx *v1.SignedTransaction, blockHeight int64) (*v1.SignedTransaction, error) {
	if err := s.isValidAttestation(ctx, tx, blockHeight); err != nil {
		return nil, fmt.Errorf("invalid attestation during finalize step: %v", err)
	}

	switch t := tx.GetAttestation().Body.(type) {
	case *v1.Attestation_ValidatorRegistration:
		return tx, s.finalizeRegisterNodeAttestation(ctx, tx, blockHeight)
	case *v1.Attestation_ValidatorDeregistration:
		return tx, s.finalizeDeregisterValidatorAttestation(ctx, tx)
	default:
		return nil, fmt.Errorf("unhandled attestation: %v %T", tx, t)
	}
}

// domainSeparatedAttestationBytes binds the signature to the protocol,
// version, chain and protobuf type. The leading zero is not a valid protobuf
// field tag, so this payload cannot also be a legacy raw protobuf message.
func domainSeparatedAttestationBytes(chainID string, body proto.Message) ([]byte, error) {
	if body == nil || !body.ProtoReflect().IsValid() {
		return nil, errors.New("empty attestation body")
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(body)
	if err != nil {
		return nil, err
	}
	data := []byte("\x00openaudio/attestation/v1\x00")
	data = binary.BigEndian.AppendUint64(data, uint64(len(chainID)))
	data = append(data, chainID...)
	data = append(data, string(body.ProtoReflect().Descriptor().FullName())...)
	data = append(data, 0)
	return append(data, payload...), nil
}

// RPC signers use the next block's rules; consensus uses the block being
// validated/replayed. Never fall back to legacy signatures after activation.
func (s *Server) validatorAttestationBytes(body proto.Message, blockHeight int64) ([]byte, error) {
	if !s.config.Upgrades.RulesetAt(blockHeight).AttestationDomainEnforced {
		return proto.Marshal(body)
	}
	if s.config.GenesisFile == nil || s.config.GenesisFile.ChainID == "" {
		return nil, errors.New("missing attestation chain ID")
	}
	return domainSeparatedAttestationBytes(s.config.GenesisFile.ChainID, body)
}

func recoverSigners(signatures []string, data []byte) ([]string, error) {
	res := make([]string, len(signatures))
	type result struct {
		index   int
		address string
		err     error
	}

	ch := make(chan result, len(signatures))
	var wg sync.WaitGroup
	for i, sig := range signatures {
		wg.Add(1)
		go func(i int, sig string) {
			defer wg.Done()
			_, address, err := common.EthRecover(sig, data)
			ch <- result{i, address, err}
		}(i, sig)
	}

	wg.Wait()
	close(ch)

	for r := range ch {
		if r.err != nil {
			return nil, fmt.Errorf("invalid signature provided to attestation tx: %v", r.err)
		}
		res[r.index] = r.address
	}

	return res, nil
}

// param excludeAddress is for attestations that should exclude a particular signer, e.g. Deregistrations. Set to "" if unneeded.
func (s *Server) attestationHasEnoughSigners(ctx context.Context, signers []string, rendezvousKey []byte, rendezvousSize, signersNeeded int, excludeAddress string) (bool, error) {
	addrs, err := s.db.GetAllEthAddressesOfRegisteredNodes(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("failed to get core validators while validating registration: %v", err)
	}

	filteredAddrs := addrs[:]
	for i, addr := range addrs {
		if addr == excludeAddress { // delete (in-place) excluded address
			filteredAddrs[i] = filteredAddrs[len(filteredAddrs)-1]
			filteredAddrs = filteredAddrs[:len(filteredAddrs)-1]
			break
		}
	}
	signersNeeded = min(len(filteredAddrs), signersNeeded)
	rendezvous := common.GetAttestorRendezvous(filteredAddrs, rendezvousKey, rendezvousSize)
	for _, address := range signers {
		if rendezvous[address] {
			signersNeeded--
			delete(rendezvous, address)
		}
	}
	if signersNeeded > 0 {
		s.logger.Info(
			"not enough attestations",
			zap.Strings("signers", signers),
			zap.Int("count", len(signers)),
			zap.Int("signers needed", signersNeeded),
			zap.Int("needed remaining", signersNeeded),
			zap.Int("rendezvous size", rendezvousSize),
			zap.Int("filtered addrs", len(filteredAddrs)),
			zap.Strings("rendezvous", slices.Collect(maps.Keys(rendezvous))),
		)
		return false, nil
	}
	return true, nil
}
