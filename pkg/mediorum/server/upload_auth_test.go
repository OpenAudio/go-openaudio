package server

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	coreServer "github.com/OpenAudio/go-openaudio/pkg/core/server"
	"github.com/ethereum/go-ethereum/crypto"
	"go.uber.org/zap"
)

func testKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key, strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
}

// signedUploadMetadata produces the tus metadata a signing client sends.
func signedUploadMetadata(t *testing.T, key *ecdsa.PrivateKey, userID int64, ts time.Time) map[string]string {
	t.Helper()
	timestamp := ts.UnixMilli()

	digest, err := coreServer.UploadRequestHash(userID, timestamp)
	if err != nil {
		t.Fatalf("hashing upload request: %v", err)
	}
	sig, err := crypto.Sign(digest, key)
	if err != nil {
		t.Fatalf("signing upload request: %v", err)
	}
	// Wallets emit v as 27/28; mirror that so the verifier's normalization is
	// exercised rather than bypassed.
	sig[64] += 27

	return map[string]string{
		"signature": "0x" + hex.EncodeToString(sig),
		"userId":    strconv.FormatInt(userID, 10),
		"timestamp": strconv.FormatInt(timestamp, 10),
	}
}

func TestVerifyUploadSignatureRecoversWalletAndUser(t *testing.T) {
	key, wallet := testKey(t)

	got, err := verifyUploadSignature(signedUploadMetadata(t, key, 4242, time.Now()))
	if err != nil {
		t.Fatalf("expected signature to verify: %v", err)
	}
	if got.Wallet != wallet {
		t.Fatalf("expected wallet %s, got %s", wallet, got.Wallet)
	}
	if got.UserID != 4242 {
		t.Fatalf("expected user id 4242, got %d", got.UserID)
	}
}

func TestVerifyUploadSignatureRejectsMissingAndMalformed(t *testing.T) {
	key, _ := testKey(t)
	valid := signedUploadMetadata(t, key, 1, time.Now())

	cases := map[string]map[string]string{
		"absent":          {},
		"empty":           {"signature": ""},
		"not hex":         {"signature": "definitely-not-a-signature", "userId": "1", "timestamp": valid["timestamp"]},
		"no user id":      {"signature": valid["signature"], "timestamp": valid["timestamp"]},
		"zero user id":    {"signature": valid["signature"], "userId": "0", "timestamp": valid["timestamp"]},
		"no timestamp":    {"signature": valid["signature"], "userId": "1"},
		"short signature": {"signature": "0xdeadbeef", "userId": "1", "timestamp": valid["timestamp"]},
	}
	for name, meta := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := verifyUploadSignature(meta); err == nil {
				t.Fatal("expected verification to fail")
			}
		})
	}
}

// A captured signature must stop working, or it is a permanent credential for
// uploading as someone else.
func TestVerifyUploadSignatureRejectsStale(t *testing.T) {
	key, _ := testKey(t)
	meta := signedUploadMetadata(t, key, 1, time.Now().Add(-uploadSignatureMaxAge-time.Minute))

	if _, err := verifyUploadSignature(meta); err == nil {
		t.Fatal("expected a stale signature to be rejected")
	}
}

// A future timestamp yields a negative age, which a one-sided age check would
// wave through as never expiring.
func TestVerifyUploadSignatureRejectsFutureDated(t *testing.T) {
	key, _ := testKey(t)
	meta := signedUploadMetadata(t, key, 1, time.Now().Add(24*time.Hour))

	if _, err := verifyUploadSignature(meta); err == nil {
		t.Fatal("expected a future-dated signature to be rejected")
	}
}

// userId and timestamp travel unsigned in tus metadata, but they are
// reproduced inside the typed data, so altering either changes the digest and
// recovery no longer yields the signer's wallet.
func TestVerifyUploadSignatureDetectsTamperedFields(t *testing.T) {
	key, wallet := testKey(t)

	for _, field := range []string{"userId", "timestamp"} {
		t.Run(field, func(t *testing.T) {
			meta := signedUploadMetadata(t, key, 7, time.Now())
			switch field {
			case "userId":
				meta["userId"] = "999999"
			case "timestamp":
				meta["timestamp"] = strconv.FormatInt(time.Now().Add(-time.Minute).UnixMilli(), 10)
			}

			got, err := verifyUploadSignature(meta)
			if err != nil {
				return // recovery failing outright is fine
			}
			if got.Wallet == wallet {
				t.Fatalf("tampering with %s must not still recover the signer", field)
			}
		})
	}
}

// Signup uploads a profile picture before the account has a user id, and
// images are served unauthenticated anyway, so image templates stay open.
func TestResolveUploadIdentitySkipsImageTemplates(t *testing.T) {
	ss := &MediorumServer{}
	ss.Config.ContentAuthEnabled = true

	for _, tmpl := range []JobTemplate{JobTemplateImgSquare, JobTemplateImgBackdrop} {
		got, err := ss.resolveUploadIdentity(tmpl, map[string]string{})
		if err != nil {
			t.Fatalf("%s should not require a signature: %v", tmpl, err)
		}
		if got.Wallet != "" {
			t.Fatalf("%s should not resolve a wallet", tmpl)
		}
	}
}

func TestResolveUploadIdentityRequiresSignedAudioWhenEnforcing(t *testing.T) {
	ss := &MediorumServer{}
	ss.Config.ContentAuthEnabled = true

	if _, err := ss.resolveUploadIdentity(JobTemplateAudio, map[string]string{}); err == nil {
		t.Fatal("expected unsigned audio to be rejected when enforcing")
	}
}

// Before enforcement, unsigned audio still uploads — it just never earns an
// attestation, so it cannot later claim its cids.
func TestResolveUploadIdentityAllowsUnsignedAudioWhenNotEnforcing(t *testing.T) {
	ss := &MediorumServer{}
	ss.Config.ContentAuthEnabled = false

	got, err := ss.resolveUploadIdentity(JobTemplateAudio, map[string]string{})
	if err != nil {
		t.Fatalf("expected unsigned audio to be allowed: %v", err)
	}
	if got.Wallet != "" {
		t.Fatal("an unsigned upload must not be credited to any wallet")
	}
}

// A signature that is offered and does not verify is an error either way.
// Treating it as "unsigned" would let a bad signature pass as anonymous.
func TestResolveUploadIdentityRejectsBadSignatureEvenWhenNotEnforcing(t *testing.T) {
	ss := &MediorumServer{}
	ss.Config.ContentAuthEnabled = false

	meta := map[string]string{"signature": "0xgarbage", "userId": "1", "timestamp": "1"}
	if _, err := ss.resolveUploadIdentity(JobTemplateAudio, meta); err == nil {
		t.Fatal("expected an invalid signature to be rejected")
	}
}

// Content authorization must not be reachable only on networks that also run
// DDEX; the two flags are deliberately independent.
func TestContentAuthIsIndependentOfProgrammableDistribution(t *testing.T) {
	ss := &MediorumServer{}
	ss.Config.ProgrammableDistributionEnabled = false
	ss.Config.ContentAuthEnabled = true

	if !ss.contentAuthEnabled() {
		t.Fatal("content auth must not depend on the programmable-distribution flag")
	}
}

func signedUpload() *Upload {
	return &Upload{
		ID:              "up1",
		UserWallet:      nullString("0xUpLoAdEr"),
		UserID:          nullInt64(7),
		UploadSignature: nullString("0xsig"),
	}
}

// Every cid an upload produces has to be covered, and the payload is keyed on
// the user rather than the uploading wallet — see content_auth_state.go.
func TestContentAttestationForCoversEveryCid(t *testing.T) {
	ss := &MediorumServer{logger: zap.NewNop()}
	ss.Config.ContentAuthEnabled = true

	ca := ss.contentAttestationFor(signedUpload(), []string{"orig", "", "320", "preview"})
	if ca == nil {
		t.Fatal("expected an attestation")
	}
	if got := strings.Join(ca.Cids, ","); got != "orig,320,preview" {
		t.Fatalf("expected empty cids dropped and the rest kept in order, got %q", got)
	}
	if ca.UserId != 7 {
		t.Fatalf("expected the attestation keyed on the user, got %d", ca.UserId)
	}
	if ca.UploaderAddress != "0xuploader" {
		t.Fatalf("expected a lowercased uploader address, got %q", ca.UploaderAddress)
	}
}

// These are the cases where no claim was ever possible. They must read as
// "nothing to wait for" so they never hold an upload back from reporting done.
func TestContentAttestationForSkipsWhenNoClaimIsPossible(t *testing.T) {
	unsigned := signedUpload()
	unsigned.UploadSignature = nullString("")
	noWallet := signedUpload()
	noWallet.UserWallet = nullString("")
	noUser := signedUpload()
	noUser.UserID = nullInt64(0)

	cases := []struct {
		name    string
		enabled bool
		upload  *Upload
		cids    []string
	}{
		{"content auth disabled", false, signedUpload(), []string{"orig"}},
		{"no cids", true, signedUpload(), nil},
		{"only empty cids", true, signedUpload(), []string{"", ""}},
		{"unsigned upload", true, unsigned, []string{"orig"}},
		{"no wallet", true, noWallet, []string{"orig"}},
		{"no user id", true, noUser, []string{"orig"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ss := &MediorumServer{logger: zap.NewNop()}
			ss.Config.ContentAuthEnabled = tc.enabled
			if ca := ss.contentAttestationFor(tc.upload, tc.cids); ca != nil {
				t.Fatalf("expected no attestation, got %v", ca.Cids)
			}
			if err := ss.attestUploadCids(context.Background(), tc.upload, tc.cids...); err != nil {
				t.Fatalf("a skipped attestation must not block the upload: %v", err)
			}
		})
	}
}
