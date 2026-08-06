package server

import (
	"strings"
	"testing"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/ethereum/go-ethereum/crypto"
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
		OrigCid:          "orig",
		TranscodedCid:    "320",
		PreviewCid:       "preview",
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
		{"substituted orig cid", func(c *v1.ContentAttestation) { c.OrigCid = "victim-orig" }},
		{"substituted transcode", func(c *v1.ContentAttestation) { c.TranscodedCid = "victim-320" }},
		{"substituted preview", func(c *v1.ContentAttestation) { c.PreviewCid = "victim-preview" }},
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
	lower, err := ContentAttestationBytes(&v1.ContentAttestation{UploaderAddress: "0xabcdef", OrigCid: "c"})
	if err != nil {
		t.Fatal(err)
	}
	upper, err := ContentAttestationBytes(&v1.ContentAttestation{UploaderAddress: "0xABCDEF", OrigCid: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if string(lower) != string(upper) {
		t.Fatal("uploader address casing must not change the signed bytes")
	}
}
