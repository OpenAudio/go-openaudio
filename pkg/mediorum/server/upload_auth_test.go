package server

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// Signup uploads a profile picture before the account has a user id, and
// images are served unauthenticated anyway, so image templates stay open.
func TestResolveUploadUserIDSkipsImageTemplates(t *testing.T) {
	ss := &MediorumServer{}
	ss.Config.ContentAuthEnabled = true

	for _, tmpl := range []JobTemplate{JobTemplateImgSquare, JobTemplateImgBackdrop} {
		got, err := ss.resolveUploadUserID(tmpl, map[string]string{})
		if err != nil {
			t.Fatalf("%s should not require a user id: %v", tmpl, err)
		}
		if got != 0 {
			t.Fatalf("%s should not resolve a user id", tmpl)
		}
	}
}

// An audio upload with no user id could never earn an attestation, so under
// enforcement it fails at create rather than at publish.
func TestResolveUploadUserIDRequiresUserIDWhenEnforcing(t *testing.T) {
	ss := &MediorumServer{}
	ss.Config.ContentAuthEnabled = true

	if _, err := ss.resolveUploadUserID(JobTemplateAudio, map[string]string{}); err == nil {
		t.Fatal("expected unattributed audio to be rejected when enforcing")
	}
}

// Before enforcement, unattributed audio still uploads — it just never earns
// an attestation, so it cannot later claim its cids.
func TestResolveUploadUserIDAllowsMissingUserIDWhenNotEnforcing(t *testing.T) {
	ss := &MediorumServer{}
	ss.Config.ContentAuthEnabled = false

	got, err := ss.resolveUploadUserID(JobTemplateAudio, map[string]string{})
	if err != nil {
		t.Fatalf("expected unattributed audio to be allowed: %v", err)
	}
	if got != 0 {
		t.Fatal("an unattributed upload must not be credited to any user")
	}
}

// A user id that is offered and does not parse is an error either way.
// Treating it as "absent" would let a bad assertion pass as anonymous.
func TestResolveUploadUserIDRejectsMalformedUserID(t *testing.T) {
	for _, enforcing := range []bool{true, false} {
		for _, raw := range []string{"not-a-number", "0", "-3", "1.5"} {
			ss := &MediorumServer{}
			ss.Config.ContentAuthEnabled = enforcing

			meta := map[string]string{"userId": raw}
			if _, err := ss.resolveUploadUserID(JobTemplateAudio, meta); err == nil {
				t.Fatalf("expected user id %q to be rejected (enforcing=%v)", raw, enforcing)
			}
		}
	}
}

func TestResolveUploadUserIDParsesAssertedUser(t *testing.T) {
	ss := &MediorumServer{}
	ss.Config.ContentAuthEnabled = true

	got, err := ss.resolveUploadUserID(JobTemplateAudio, map[string]string{"userId": "4242"})
	if err != nil {
		t.Fatalf("expected the asserted user to resolve: %v", err)
	}
	if got != 4242 {
		t.Fatalf("expected user id 4242, got %d", got)
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

func attributedUpload() *Upload {
	return &Upload{
		ID:     "up1",
		UserID: nullInt64(7),
	}
}

// Every cid an upload produces has to be covered, and the payload is keyed on
// the asserted user (see content_auth_state.go on why claims key on the user
// rather than any wallet).
func TestContentAttestationForCoversEveryCid(t *testing.T) {
	ss := &MediorumServer{logger: zap.NewNop()}
	ss.Config.ContentAuthEnabled = true

	ca := ss.contentAttestationFor(attributedUpload(), []string{"orig", "", "320", "preview"})
	if ca == nil {
		t.Fatal("expected an attestation")
	}
	if got := strings.Join(ca.Cids, ","); got != "orig,320,preview" {
		t.Fatalf("expected empty cids dropped and the rest kept in order, got %q", got)
	}
	if ca.UserId != 7 {
		t.Fatalf("expected the attestation keyed on the user, got %d", ca.UserId)
	}
}

// These are the cases where no claim was ever possible. They must read as
// "nothing to wait for" so they never hold an upload back from reporting done.
func TestContentAttestationForSkipsWhenNoClaimIsPossible(t *testing.T) {
	noUser := attributedUpload()
	noUser.UserID = nullInt64(0)

	cases := []struct {
		name    string
		enabled bool
		upload  *Upload
		cids    []string
	}{
		{"content auth disabled", false, attributedUpload(), []string{"orig"}},
		{"no cids", true, attributedUpload(), nil},
		{"only empty cids", true, attributedUpload(), []string{"", ""}},
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
