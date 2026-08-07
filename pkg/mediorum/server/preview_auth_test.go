package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// A preview request carries the same signed fields as a tus upload, moved to
// the query string.
func signedPreviewRequest(t *testing.T, userID int64) map[string]string {
	t.Helper()
	key, _ := testKey(t)
	return signedUploadMetadata(t, key, userID, time.Now())
}

// With content auth off the endpoint behaves as it always has: no signature
// required, and nothing attested.
func TestPreviewClaimantSkipsWhenContentAuthDisabled(t *testing.T) {
	ss := &MediorumServer{logger: zap.NewNop()}
	ss.Config.ContentAuthEnabled = false

	got, err := ss.previewClaimant(context.Background(), "sourcecid", map[string]string{})
	if err != nil {
		t.Fatalf("expected an unsigned request to be allowed: %v", err)
	}
	if got != 0 {
		t.Fatalf("expected no claimant, got %d", got)
	}
}

// An unsigned or unverifiable request is refused before any transcode work. The
// endpoint is otherwise an unauthenticated ffmpeg trigger over arbitrary cids.
func TestPreviewClaimantRequiresAValidSignature(t *testing.T) {
	cases := []struct {
		name string
		meta map[string]string
	}{
		{"unsigned", map[string]string{}},
		{"garbage signature", map[string]string{"signature": "0xgarbage", "userId": "1", "timestamp": "1"}},
		{"no user id", map[string]string{"signature": "0xabc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ss := &MediorumServer{logger: zap.NewNop()}
			ss.Config.ContentAuthEnabled = true

			_, err := ss.previewClaimant(context.Background(), "sourcecid", tc.meta)
			if err == nil {
				t.Fatal("expected the request to be refused")
			}
			if !strings.Contains(err.Error(), "signed") {
				t.Fatalf("expected a signature error, got %v", err)
			}
		})
	}
}

// A well-signed request still cannot proceed when the claim is uncheckable —
// crediting the caller unchecked is the attack. It reports as a node problem,
// not a caller one, so the client retries rather than re-signing.
func TestPreviewClaimantRefusesWhenClaimIsUnverifiable(t *testing.T) {
	ss := &MediorumServer{logger: zap.NewNop()}
	ss.Config.ContentAuthEnabled = true

	_, err := ss.previewClaimant(context.Background(), "sourcecid", signedPreviewRequest(t, 7))
	if !errors.Is(err, errPreviewUnverifiable) {
		t.Fatalf("expected an unverifiable-claim error, got %v", err)
	}
}

// A preview attestation credits the user with no uploader attached: this node
// produced the bytes, so no wallet uploaded them.
func TestContentAttestationForPreviewHasNoUploader(t *testing.T) {
	ca := contentAttestation(7, "previewcid", "0xValidator")
	if ca.UserId != 7 || len(ca.Cids) != 1 || ca.Cids[0] != "previewcid" {
		t.Fatalf("unexpected attestation: %+v", ca)
	}
	if ca.UploaderAddress != "" || ca.UploaderSignature != "" {
		t.Fatal("a generated preview has no uploader to record")
	}
}
