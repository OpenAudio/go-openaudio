package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/ethereum/go-ethereum/crypto"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// recoverAttestationSigner mirrors the recovery half of
// isValidContentAttestation, isolated so the signature scheme can be exercised
// without a database.
func recoverAttestationSigner(ca *v1.ContentAttestation) (string, error) {
	payload, err := ContentAttestationBytes(ca)
	if err != nil {
		return "", err
	}
	_, addr, err := common.EthRecover(ca.GetValidatorSignature(), payload)
	return addr, err
}

func signedAttestation(t *testing.T) (*v1.ContentAttestation, string) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validator := crypto.PubkeyToAddress(key.PublicKey).Hex()

	ca := &v1.ContentAttestation{
		UploaderAddress:  "0xuploader",
		Cids:             []string{"orig", "320", "preview"},
		ValidatorAddress: validator,
	}
	payload, err := ContentAttestationBytes(ca)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := common.EthSign(key, payload)
	if err != nil {
		t.Fatal(err)
	}
	ca.ValidatorSignature = sig
	return ca, validator
}

func TestContentAttestationRecoversValidator(t *testing.T) {
	ca, validator := signedAttestation(t)

	got, err := recoverAttestationSigner(ca)
	if err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
	if !strings.EqualFold(got, validator) {
		t.Fatalf("expected %s, recovered %s", validator, got)
	}
}

// Attestations are public once on chain, so an attacker can lift a validator's
// signature. Binding the uploader into the signed payload makes the lifted
// signature recover to a different address, which then fails the
// registered-validator check.
func TestContentAttestationIsBoundToUploader(t *testing.T) {
	ca, validator := signedAttestation(t)

	forged := proto.Clone(ca).(*v1.ContentAttestation)
	forged.UploaderAddress = "0xattacker"

	got, err := recoverAttestationSigner(forged)
	if err != nil {
		// Recovery failing outright is an equally good outcome.
		return
	}
	if strings.EqualFold(got, validator) {
		t.Fatal("a signature bound to one uploader must not verify for another")
	}
}

// The binding must hold for every field the attestation covers, so a signature
// cannot be replayed against a substituted cid or upload.
func TestContentAttestationIsBoundToEveryField(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*v1.ContentAttestation)
	}{
		{"substituted cid", func(c *v1.ContentAttestation) { c.Cids = []string{"orig", "320", "victim-preview"} }},
		{"appended cid", func(c *v1.ContentAttestation) { c.Cids = append(c.Cids, "victim-cid") }},
		{"reordered cids", func(c *v1.ContentAttestation) { c.Cids = []string{"320", "orig", "preview"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ca, validator := signedAttestation(t)
			tampered := proto.Clone(ca).(*v1.ContentAttestation)
			tc.mutate(tampered)

			got, err := recoverAttestationSigner(tampered)
			if err != nil {
				return
			}
			if strings.EqualFold(got, validator) {
				t.Fatalf("%s must invalidate the attestation", tc.name)
			}
		})
	}
}

// The uploader signature is forensic only. Changing it must not affect
// validity, or a future change to the client's signing format would
// retroactively invalidate attestations on replay.
func TestContentAttestationIgnoresUploaderSignature(t *testing.T) {
	ca, validator := signedAttestation(t)
	ca.UploaderSignature = "0xwhatever-the-client-presented"

	got, err := recoverAttestationSigner(ca)
	if err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
	if !strings.EqualFold(got, validator) {
		t.Fatal("uploader signature must not participate in the validator signature")
	}
}

// Address casing is a checksum, not identity. The payload lowercases the
// uploader so signer and verifier cannot disagree over it.
func TestContentAttestationBytesNormalizeUploaderCasing(t *testing.T) {
	lower, err := ContentAttestationBytes(&v1.ContentAttestation{UploaderAddress: "0xabcdef", Cids: []string{"c"}})
	if err != nil {
		t.Fatal(err)
	}
	upper, err := ContentAttestationBytes(&v1.ContentAttestation{UploaderAddress: "0xABCDEF", Cids: []string{"c"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(lower) != string(upper) {
		t.Fatal("uploader address casing must not change the signed bytes")
	}
}

// Pre-gate, finalize must error rather than succeed. A binary predating this
// transaction type falls to finalizeTransaction's default case and errors,
// yielding ExecTxResult Code 2; CometBFT folds result codes into the header's
// LastResultsHash, so an upgraded node that succeeded here would compute a
// different header and stall the chain.
//
// This is the invariant most likely to be "simplified" into a no-op return,
// which is exactly what would reintroduce the halt.
func TestContentAttestationFinalizeErrorsBeforeGate(t *testing.T) {
	s := &Server{
		config: &config.Config{Upgrades: &config.UpgradeSchedule{}}, // nothing scheduled
		logger: zap.NewNop(),
	}
	ca, _ := signedAttestation(t)
	tx := &v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_ContentAttestation{ContentAttestation: ca},
	}

	if _, err := s.finalizeContentAttestation(context.Background(), tx, "tx1", 100); err == nil {
		t.Fatal("finalize must error before the gate so the result code matches a node that predates this type")
	}
}

// And the mirror: block validity must NOT reject pre-gate, because that same
// pre-type binary falls through its switch and votes the block valid.
func TestContentAttestationBlockValidityAcceptsBeforeGate(t *testing.T) {
	s := &Server{config: &config.Config{Upgrades: &config.UpgradeSchedule{}}, logger: zap.NewNop()}
	ca, _ := signedAttestation(t)
	txBytes, err := proto.Marshal(&v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_ContentAttestation{ContentAttestation: ca},
	})
	if err != nil {
		t.Fatal(err)
	}

	valid, err := s.validateBlockTx(context.Background(), config.Rules{}, newOverlayAuthStore(newMemAuthStore()),
		time.Time{}, 100, nil, txBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !valid {
		t.Fatal("a block carrying a pre-gate attestation must stay valid, or upgraded nodes fork from un-upgraded ones")
	}
}

// ProcessProposal must distinguish "this attestation is bad" from "this node
// cannot tell". Voting a block invalid because the local database was briefly
// unreachable disagrees with healthy peers over a proposal that is fine.
func TestContentAttestationRejectionsAreDistinguishableFromStoreFailures(t *testing.T) {
	deterministic := []struct {
		name string
		ca   *v1.ContentAttestation
	}{
		{"no cids", &v1.ContentAttestation{UploaderAddress: "0xup", UserId: 1, ValidatorAddress: "0xv", ValidatorSignature: "0xs"}},
		{"no user id", &v1.ContentAttestation{UploaderAddress: "0xup", Cids: []string{"c"}, ValidatorAddress: "0xv", ValidatorSignature: "0xs"}},
		{"empty cid", &v1.ContentAttestation{UploaderAddress: "0xup", UserId: 1, Cids: []string{""}, ValidatorAddress: "0xv", ValidatorSignature: "0xs"}},
		{"no validator signature", &v1.ContentAttestation{UploaderAddress: "0xup", UserId: 1, Cids: []string{"c"}, ValidatorAddress: "0xv"}},
		{"unrecoverable signature", &v1.ContentAttestation{UploaderAddress: "0xup", UserId: 1, Cids: []string{"c"}, ValidatorAddress: "0xv", ValidatorSignature: "0xgarbage"}},
	}

	s := &Server{}
	for _, tc := range deterministic {
		t.Run(tc.name, func(t *testing.T) {
			err := s.isValidContentAttestation(context.Background(),
				&v1.SignedTransaction{Transaction: &v1.SignedTransaction_ContentAttestation{ContentAttestation: tc.ca}})
			if err == nil {
				t.Fatal("expected a rejection")
			}
			if !authRejected(err) {
				t.Fatalf("a deterministic rejection must be recognizable as one, got %v", err)
			}
		})
	}
}

// A bare error means the node could not decide, and must not be mistaken for a
// verdict on the transaction.
func TestStoreFailureIsNotAnAuthRejection(t *testing.T) {
	if authRejected(errors.New("connection refused")) {
		t.Fatal("a store failure must not read as a deterministic rejection")
	}
}
