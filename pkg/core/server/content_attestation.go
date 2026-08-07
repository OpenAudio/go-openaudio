package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// ContentAttestation transactions record that a wallet uploaded a set of bytes
// to a validator. See content_auth_state.go for what the resulting claims are
// used for, and the proto for why this is separate from FileUpload.

// ContentAttestationBytes builds the payload a validator signs. Storage nodes
// sign these bytes and consensus verifies them, so both derive them here rather
// than marshalling separately.
func ContentAttestationBytes(ca *v1.ContentAttestation) ([]byte, error) {
	return proto.Marshal(&v1.ContentAttestationPayload{
		UserId:          ca.GetUserId(),
		Cids:            ca.GetCids(),
		UploaderAddress: strings.ToLower(ca.GetUploaderAddress()),
	})
}

// isValidContentAttestation checks the attestation is signed by a registered
// validator over exactly the fields it claims.
//
// UploaderSignature is deliberately unverified: it is produced before any cid
// exists, so it names no content and proves nothing about who uploaded these
// bytes. See the proto.
//
// Deterministic rejections come back as txRejectionError; a store failure comes
// back bare. Proposal validation depends on the difference — voting a block
// invalid because this node could not reach its database would disagree with
// healthy peers over a proposal that is fine.
func (s *Server) isValidContentAttestation(ctx context.Context, tx *v1.SignedTransaction) error {
	ca := tx.GetContentAttestation()
	if ca == nil {
		return txRejectedf("content attestation not present")
	}

	if ca.GetUploaderAddress() == "" {
		return txRejectedf("no uploader address provided")
	}
	if ca.GetUserId() == 0 {
		return txRejectedf("no user id provided")
	}
	if len(ca.GetCids()) == 0 {
		return txRejectedf("no cids provided")
	}
	for _, cid := range ca.GetCids() {
		if cid == "" {
			return txRejectedf("empty cid provided")
		}
	}

	validatorAddress := ca.GetValidatorAddress()
	if validatorAddress == "" {
		return txRejectedf("no validator address provided")
	}
	sig := ca.GetValidatorSignature()
	if sig == "" {
		return txRejectedf("no validator signature provided")
	}

	payload, err := ContentAttestationBytes(ca)
	if err != nil {
		return txRejectedf("could not marshal attestation payload: %v", err)
	}

	_, recovered, err := common.EthRecover(sig, payload)
	if err != nil {
		return txRejectedf("could not recover attestation signer: %v", err)
	}
	if !strings.EqualFold(recovered, validatorAddress) {
		return txRejectedf("validator address and attestation signer mismatch: expected %s, got %s", validatorAddress, recovered)
	}

	// Only a registered node's word counts: anyone can generate a keypair and
	// sign, so what makes an attestation meaningful is that the signer is a
	// staked participant that actually receives uploads.
	//
	// Jailed validators count too — see the query comment. Filtering on
	// time-varying state here would make replay disagree with the original
	// execution.
	// Bare, not a rejection: this is the one branch here that can fail for
	// reasons local to this node.
	registered, err := s.db.IsRegisteredNodeEthAddress(ctx, validatorAddress)
	if err != nil {
		return fmt.Errorf("could not look up validator %s: %w", validatorAddress, err)
	}
	if !registered {
		return txRejectedf("validator %s is not a registered node", validatorAddress)
	}
	return nil
}

// finalizeContentAttestation records the claims the attestation establishes.
//
// Pre-gate this errors rather than succeeding, which is not the same as
// ignoring it. A binary predating this transaction type falls to
// finalizeTransaction's default case and errors; CometBFT folds result codes
// into the header's LastResultsHash, so an upgraded node that succeeded here
// would compute a different header and stall the chain. Erroring reproduces the
// old code exactly, and as a side effect writes no claims — which is what stops
// an attacker seeding core_auth_cids ahead of enforcement.
func (s *Server) finalizeContentAttestation(ctx context.Context, tx *v1.SignedTransaction, txHash string, blockHeight int64) (proto.Message, error) {
	if !s.config.Upgrades.RulesetAt(blockHeight).ContentAuthEnforced {
		return nil, errors.New("content attestation before content auth is active")
	}

	if err := s.isValidContentAttestation(ctx, tx); err != nil {
		s.logger.Error("invalid content attestation", zap.Error(err))
		return nil, err
	}

	ca := tx.GetContentAttestation()
	if err := projectContentAttestationCids(ctx, &dbAuthStore{q: s.getDb()}, ca, txHash); err != nil {
		return nil, fmt.Errorf("could not project content attestation cids: %w", err)
	}

	return nil, nil
}
