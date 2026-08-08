package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

// With content auth off the endpoint behaves as it always has: no user id
// required, and nothing attested — even when one is asserted.
func TestPreviewClaimantSkipsWhenContentAuthDisabled(t *testing.T) {
	ss := &MediorumServer{logger: zap.NewNop()}
	ss.Config.ContentAuthEnabled = false

	for _, userID := range []int64{0, 7} {
		got, err := ss.previewClaimant(context.Background(), "sourcecid", userID)
		if err != nil {
			t.Fatalf("expected the request to be allowed: %v", err)
		}
		if got != 0 {
			t.Fatalf("expected no claimant, got %d", got)
		}
	}
}

// A request with no asserted user is refused before any transcode work: the
// preview it would produce could never be attested, and the endpoint is
// otherwise an unauthenticated ffmpeg trigger over arbitrary cids.
func TestPreviewClaimantRequiresAUserID(t *testing.T) {
	ss := &MediorumServer{logger: zap.NewNop()}
	ss.Config.ContentAuthEnabled = true

	_, err := ss.previewClaimant(context.Background(), "sourcecid", 0)
	if err == nil {
		t.Fatal("expected the request to be refused")
	}
	if !strings.Contains(err.Error(), "user") {
		t.Fatalf("expected a user id error, got %v", err)
	}
}

// An asserted user still cannot proceed when the claim is uncheckable —
// crediting them unchecked is the attack. It reports as a node problem, not a
// caller one, so the client retries rather than reasserting.
func TestPreviewClaimantRefusesWhenClaimIsUnverifiable(t *testing.T) {
	ss := &MediorumServer{logger: zap.NewNop()}
	ss.Config.ContentAuthEnabled = true

	_, err := ss.previewClaimant(context.Background(), "sourcecid", 7)
	if !errors.Is(err, errPreviewUnverifiable) {
		t.Fatalf("expected an unverifiable-claim error, got %v", err)
	}
}

// Absent means 0 so previewClaimant can apply the content-auth rules;
// malformed is an error regardless, so a bad assertion cannot masquerade as no
// assertion.
func TestPreviewRequestUserIDParsing(t *testing.T) {
	newCtx := func(query string) echo.Context {
		req := httptest.NewRequest("POST", "/generate_preview/cid/30"+query, nil)
		return echo.New().NewContext(req, httptest.NewRecorder())
	}

	if got, err := newCtx("").QueryParam("userId"), ""; got != err {
		t.Fatalf("test setup: expected empty query param, got %q", got)
	}

	userID, err := previewRequestUserID(newCtx(""))
	if err != nil || userID != 0 {
		t.Fatalf("expected an absent user id to read as 0, got %d, %v", userID, err)
	}

	userID, err = previewRequestUserID(newCtx("?userId=42"))
	if err != nil || userID != 42 {
		t.Fatalf("expected user id 42, got %d, %v", userID, err)
	}

	for _, raw := range []string{"abc", "0", "-3", "1.5"} {
		if _, err := previewRequestUserID(newCtx("?userId=" + raw)); err == nil {
			t.Fatalf("expected user id %q to be rejected", raw)
		}
	}
}

// A preview attestation credits the user for exactly the one cid this node
// produced.
func TestContentAttestationForPreview(t *testing.T) {
	ca := contentAttestation(7, "previewcid", "0xValidator")
	if ca.UserId != 7 || len(ca.Cids) != 1 || ca.Cids[0] != "previewcid" {
		t.Fatalf("unexpected attestation: %+v", ca)
	}
}
