package server

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/core/gen/core_proto"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
)

func (s *Server) isValidAttestation(ctx context.Context, tx *core_proto.SignedTransaction, blockHeight int64) error {
	att := tx.GetAttestation()
	if att == nil {
		return errors.New("empty attestation tx")
	}

	bodyBytes, err := getAttestationBodyBytes(att)
	if err != nil {
		return fmt.Errorf("could not marshal attestation body into bytes: %v", err)
	}
	signerAddrs, err := recoverSigners(att.Signatures, bodyBytes)
	if err != nil {
		return fmt.Errorf("could not recover signers: %v", err)
	}

	switch t := att.Body.(type) {
	case *core_proto.Attestation_ValidatorRegistration:
		return s.isValidRegisterNodeAttestation(ctx, tx, signerAddrs, blockHeight)
	case *core_proto.Attestation_ValidatorDeregistration:
		return s.isValidDeregisterNodeAttestation(ctx, tx, signerAddrs, blockHeight)
	default:
		return fmt.Errorf("unhandled attestation: %v %T", tx, t)
	}
}

func (s *Server) finalizeAttestation(ctx context.Context, tx *core_proto.SignedTransaction, blockHeight int64) (*core_proto.SignedTransaction, error) {
	if err := s.isValidAttestation(ctx, tx, blockHeight); err != nil {
		return nil, fmt.Errorf("invalid attestation during finalize step: %v", err)
	}

	switch t := tx.GetAttestation().Body.(type) {
	case *core_proto.Attestation_ValidatorRegistration:
		return tx, s.finalizeRegisterNodeAttestation(ctx, tx, blockHeight)
	default:
		return nil, fmt.Errorf("unhandled attestation: %v %T", tx, t)
	}
}

func getAttestationBodyBytes(att *core_proto.Attestation) ([]byte, error) {
	switch t := att.Body.(type) {
	case *core_proto.Attestation_ValidatorRegistration:
		return proto.Marshal(att.GetValidatorRegistration())
	default:
		return nil, fmt.Errorf("unhandled attestation: %v %T", att, t)
	}
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

func (s *Server) attestationHasEnoughSigners(ctx context.Context, signers []string, rendezvousKey []byte, rendezvousSize, signersNeeded int) (bool, error) {
	addrs, err := s.db.GetAllEthAddressesOfRegisteredNodes(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("failed to get core validators while validating registration: %v", err)
	}
	signersNeeded = min(len(addrs), signersNeeded)
	rendezvous := common.GetAttestorRendezvous(addrs, rendezvousKey, rendezvousSize)
	for _, address := range signers {
		if rendezvous[address] {
			signersNeeded--
			delete(rendezvous, address)
		}
	}
	if signersNeeded > 0 {
		s.logger.Infof("not enough attestations. Had: %d, needed: %d more", len(signers), signersNeeded)
		return false, nil
	}
	return true, nil
}
