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

// ContentAttestation transactions: a validator stating on chain that a wallet
// uploaded a specific set of bytes to it.
//
// This is what makes content authorization possible. A track's cid fields are
// client metadata, so consensus cannot tell a genuine claim from a stolen one
// on its own — it needs a statement from the party that witnessed the bytes.
//
// Kept separate from FileUpload (files.go), which serves DDEX and is gated on
// the programmable-distribution flag. Content authorization must be able to
// activate on a network where DDEX is switched off, and the two carry
// incompatible notions of what the uploader signed.

// ContentAttestationBytes builds the exact payload a validator signs.
// Storage nodes sign these bytes and consensus verifies them, so the two must
// derive them identically — hence one shared constructor rather than a
// marshal on each side.
//
// uploader_address is lowercased inside the payload so signer and verifier
// cannot disagree over EIP-55 checksum casing, and so the signed bytes match
// the key core_auth_cids is written under.
func ContentAttestationBytes(ca *v1.ContentAttestation) ([]byte, error) {
	return proto.Marshal(&v1.ContentAttestationPayload{
		UploaderAddress: strings.ToLower(ca.GetUploaderAddress()),
		OrigCid:         ca.GetOrigCid(),
		TranscodedCid:   ca.GetTranscodedCid(),
		PreviewCid:      ca.GetPreviewCid(),
	})
}

// isValidContentAttestation checks the attestation is signed by a registered
// validator over exactly the fields it claims.
//
// fu.UploaderSignature is intentionally not checked here — see the proto
// comment. It is signed before any cid exists, so it names no content and
// proves nothing about who uploaded these bytes.
func (s *Server) isValidContentAttestation(ctx context.Context, tx *v1.SignedTransaction) error {
	ca := tx.GetContentAttestation()
	if ca == nil {
		return errors.New("content attestation not present")
	}

	if ca.GetUploaderAddress() == "" {
		return errors.New("no uploader address provided")
	}
	if ca.GetOrigCid() == "" {
		return errors.New("no orig cid provided")
	}

	validatorAddress := ca.GetValidatorAddress()
	if validatorAddress == "" {
		return errors.New("no validator address provided")
	}
	sig := ca.GetValidatorSignature()
	if sig == "" {
		return errors.New("no validator signature provided")
	}

	payload, err := ContentAttestationBytes(ca)
	if err != nil {
		return fmt.Errorf("could not marshal attestation payload: %w", err)
	}

	_, recovered, err := common.EthRecover(sig, payload)
	if err != nil {
		return fmt.Errorf("could not recover attestation signer: %w", err)
	}
	if !strings.EqualFold(recovered, validatorAddress) {
		return fmt.Errorf("validator address and attestation signer mismatch: expected %s, got %s", validatorAddress, recovered)
	}

	// Only a registered node's word counts. Anyone can generate a keypair and
	// sign an attestation; what makes it meaningful is that the signer is a
	// staked, accountable participant that actually receives uploads.
	validators, err := s.db.GetAllRegisteredNodes(ctx)
	if err != nil {
		return fmt.Errorf("could not get validators: %w", err)
	}
	for _, v := range validators {
		if strings.EqualFold(v.EthAddress, validatorAddress) {
			return nil
		}
	}
	return fmt.Errorf("validator %s is not a registered node", validatorAddress)
}

// finalizeContentAttestation records the claims the attestation establishes.
//
// Before the gate height this errors rather than succeeding, which is not the
// same thing as ignoring it.
//
// CometBFT folds each transaction's result Code into the block's
// LastResultsHash (abci.DeterministicExecTxResult keeps Code, Data, GasWanted
// and GasUsed), and that hash is part of the header. A binary predating this
// transaction type falls to finalizeTransaction's default case and errors,
// yielding Code 2. If an upgraded node succeeded here while an un-upgraded one
// errored, the two would compute different headers and the chain would stall —
// which is a real failure mode this network has hit before, not a theoretical
// one. So pre-gate the only safe behaviour is to produce the identical result
// code, and erroring is what produces it.
//
// Returning an error also means no claims are written, which is what keeps an
// attacker from seeding core_auth_cids ahead of enforcement.
func (s *Server) finalizeContentAttestation(ctx context.Context, tx *v1.SignedTransaction, blockHeight int64) (proto.Message, error) {
	if !s.config.Upgrades.RulesetAt(blockHeight).ContentAuthEnforced {
		return nil, errors.New("content attestation before content auth is active")
	}

	if err := s.isValidContentAttestation(ctx, tx); err != nil {
		s.logger.Error("invalid content attestation", zap.Error(err))
		return nil, err
	}

	ca := tx.GetContentAttestation()
	if err := projectContentAttestationCids(ctx, &dbAuthStore{q: s.getDb()}, ca, blockHeight); err != nil {
		if isAuthValidationError(err) {
			s.logger.Debug("content attestation cid projection skipped",
				zap.String("reason", err.Error()), zap.String("orig_cid", ca.GetOrigCid()))
		} else {
			return nil, fmt.Errorf("could not project content attestation cids: %w", err)
		}
	}

	return nil, nil
}
