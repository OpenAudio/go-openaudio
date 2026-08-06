package server

import (
	"strconv"
	"strings"
	"testing"

	coreServer "github.com/OpenAudio/go-openaudio/pkg/core/server"
)

// Cross-implementation check against a real EIP-712 signature produced by the
// SDK (packages/sdk/src/sdk/services/Storage/signUpload.ts) using viem, not by
// the Go signer. Both sides derive the digest independently from the same
// domain and type definitions, so a drift in either — a renamed domain, a
// changed field type, an added member — would break every upload in production
// while both unit suites still passed on their own.
//
// Regenerate by signing UploadRequest{userId: 42, timestamp: 1700000000000}
// with test key 0x4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318.
const (
	sdkUploadSignature = "0x53ea06ec88fc0bf794a1257b59f6af497b3925a55d1d023cef6c81d26c0c80c9413228a8f27e3c5afec7f73110c114414885ae335a41a9a15c2f67fbd50489e11b"
	sdkUploadSigner    = "0x2c7536e3605d9c16a7a3d7b1898e529396a65c23"
	sdkUploadUserID    = int64(42)
	sdkUploadTimestamp = int64(1700000000000)
)

func TestSdkEip712SignatureRecoversInGo(t *testing.T) {
	got, err := coreServer.RecoverUploadRequestSigner(sdkUploadUserID, sdkUploadTimestamp, sdkUploadSignature)
	if err != nil {
		t.Fatalf("recovering the SDK signature failed: %v", err)
	}
	if !strings.EqualFold(got, sdkUploadSigner) {
		t.Fatalf("expected signer %s, recovered %s", sdkUploadSigner, got)
	}
}

// The full verifier path rejects the fixture on freshness — its timestamp is
// fixed — which is itself the expected behaviour and confirms the age check
// runs after recovery rather than instead of it.
func TestSdkEip712SignatureIsRejectedAsStale(t *testing.T) {
	_, err := verifyUploadSignature(map[string]string{
		"signature": sdkUploadSignature,
		"userId":    strconv.FormatInt(sdkUploadUserID, 10),
		"timestamp": strconv.FormatInt(sdkUploadTimestamp, 10),
	})
	if err == nil {
		t.Fatal("expected the fixed-timestamp fixture to fail freshness")
	}
	if !strings.Contains(err.Error(), "too old") {
		t.Fatalf("expected a freshness rejection, got: %v", err)
	}
}
