package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	coreServer "github.com/OpenAudio/go-openaudio/pkg/core/server"
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

// The multipart and gRPC paths serve programmable distribution, whose
// releases name no user and create tracks outside content auth, so there an
// absent user id is allowed even under enforcement.
func TestResolveOptionalUploadUserIDAllowsAbsentWhenEnforcing(t *testing.T) {
	ss := &MediorumServer{}
	ss.Config.ContentAuthEnabled = true

	got, err := ss.resolveOptionalUploadUserID(JobTemplateAudio, "")
	if err != nil {
		t.Fatalf("absent user id must be allowed on the optional path: %v", err)
	}
	if got != 0 {
		t.Fatalf("absent user id resolved to %d", got)
	}
}

// A present user id is honored exactly as on tus, and a malformed one is
// still rejected: a bad assertion must not pass as no assertion.
func TestResolveOptionalUploadUserIDParsesAndRejectsMalformed(t *testing.T) {
	ss := &MediorumServer{}

	got, err := ss.resolveOptionalUploadUserID(JobTemplateAudio, "42")
	if err != nil || got != 42 {
		t.Fatalf("want 42, got %d (%v)", got, err)
	}
	for _, raw := range []string{"abc", "0", "-1", "1.5"} {
		if _, err := ss.resolveOptionalUploadUserID(JobTemplateAudio, raw); err == nil {
			t.Fatalf("user id %q should be rejected", raw)
		}
	}
	if got, err := ss.resolveOptionalUploadUserID(JobTemplateImgSquare, "abc"); err != nil || got != 0 {
		t.Fatalf("images carry no attribution: got %d (%v)", got, err)
	}
}

// A wired core that has not registered itself yet is the boot window: content
// auth is on but no attestation can be sent. Only uploads that would need one
// are turned away.
func TestCheckCanAttestDuringCoreBoot(t *testing.T) {
	ss := &MediorumServer{core: coreServer.NewCoreService()}
	ss.Config.ContentAuthEnabled = true

	if err := ss.checkCanAttest(JobTemplateAudio, 42); !errors.Is(err, errCoreNotReady) {
		t.Fatalf("attributed audio during boot: want errCoreNotReady, got %v", err)
	}
	if err := ss.checkCanAttest(JobTemplateAudio, 0); err != nil {
		t.Fatalf("unattributed audio never attests, got %v", err)
	}
	if err := ss.checkCanAttest(JobTemplateImgSquare, 42); err != nil {
		t.Fatalf("images never attest, got %v", err)
	}

	ss.Config.ContentAuthEnabled = false
	if err := ss.checkCanAttest(JobTemplateAudio, 42); err != nil {
		t.Fatalf("content auth off: nothing waits on core, got %v", err)
	}

	// No core at all is the unit-test shape, not a boot window.
	ss = &MediorumServer{}
	ss.Config.ContentAuthEnabled = true
	if err := ss.checkCanAttest(JobTemplateAudio, 42); err != nil {
		t.Fatalf("nil core: want nil, got %v", err)
	}
}

// The sender is the last line: a re-transcode or a job queued across a
// restart reaches it with no create-time gate in front, and it must fail
// cleanly so the missed-job sweep retries once core is up.
func TestSendContentAttestationDuringCoreBootFails(t *testing.T) {
	ss := &MediorumServer{core: coreServer.NewCoreService()}
	ss.Config.ContentAuthEnabled = true

	err := ss.sendContentAttestation(context.Background(), contentAttestation(42, "QmX", "0xabc"))
	if !errors.Is(err, errCoreNotReady) {
		t.Fatalf("want errCoreNotReady, got %v", err)
	}
}

func TestWaitForCore(t *testing.T) {
	// Nothing to wait for: returns at once.
	ss := &MediorumServer{logger: zap.NewNop()}
	ss.Config.ContentAuthEnabled = true
	if err := ss.waitForCore(context.Background()); err != nil {
		t.Fatalf("nil core: want nil, got %v", err)
	}

	// Core wired but not registered: blocks until the context ends.
	ss = &MediorumServer{core: coreServer.NewCoreService(), logger: zap.NewNop()}
	ss.Config.ContentAuthEnabled = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ss.waitForCore(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
