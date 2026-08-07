package server

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
)

func trackCidTx(userID, trackID int64, signer, action string, cids map[string]any) authTx {
	meta := map[string]any{"owner_id": float64(userID)}
	for k, v := range cids {
		meta[k] = v
	}
	return authTx{
		UserID: userID, EntityType: "Track", EntityID: trackID, Action: action, Signer: signer,
		meta: meta,
	}
}

func attestation(uploader, validator string, cids ...string) *v1.ContentAttestation {
	return attestationFor(1, uploader, validator, cids...)
}

// attestationFor names the user the upload was made for — the claim's key.
func attestationFor(userID int64, uploader, validator string, cids ...string) *v1.ContentAttestation {
	return &v1.ContentAttestation{
		UserId:           userID,
		UploaderAddress:  uploader,
		ValidatorAddress: validator,
		Cids:             cids,
	}
}

func mustContentAuth(t *testing.T, st authReader, tx authTx) {
	t.Helper()
	if err := validateTrackContentAuth(context.Background(), st, tx); err != nil {
		t.Fatalf("expected content auth to pass: %v", err)
	}
}

func mustRejectContentAuth(t *testing.T, st authReader, tx authTx, wantReason string) {
	t.Helper()
	err := validateTrackContentAuth(context.Background(), st, tx)
	if err == nil {
		t.Fatalf("expected content auth rejection (%s), but it passed", wantReason)
	}
	if !isAuthValidationError(err) {
		t.Fatalf("expected a rule rejection (%s), got store error: %v", wantReason, err)
	}
	if !strings.Contains(err.Error(), wantReason) {
		t.Fatalf("expected rejection containing %q, got %q", wantReason, err.Error())
	}
}

// An attestation entitles the uploader to assert every cid it covers.
func TestProjectContentAttestationCidsRecordsEveryCid(t *testing.T) {
	st := newMemAuthStore()
	fu := attestation("0xUpLoAdEr", "0xVaLiDaToR", "origcid", "320cid", "previewcid")

	if err := projectContentAttestationCids(context.Background(), st, fu, "tx1"); err != nil {
		t.Fatalf("projection failed: %v", err)
	}

	for _, cid := range []string{"origcid", "320cid", "previewcid"} {
		if _, ok := st.cids[memCidKey{cid, 1}]; !ok {
			t.Fatalf("expected %q claimed for the uploading user", cid)
		}
	}
}

// An upload with no preview must not record an empty-string claim, which would
// otherwise let any track assert an empty preview_cid against it.
func TestProjectContentAttestationCidsSkipsEmpty(t *testing.T) {
	st := newMemAuthStore()
	fu := attestation("0xuploader", "0xvalidator", "origcid", "320cid", "")

	if err := projectContentAttestationCids(context.Background(), st, fu, "tx1"); err != nil {
		t.Fatalf("projection failed: %v", err)
	}
	if _, ok := st.cids[memCidKey{"", 1}]; ok {
		t.Fatal("empty cid must not be recorded")
	}
	if len(st.cids) != 2 {
		t.Fatalf("expected 2 claims recorded, got %d", len(st.cids))
	}
}

// A cid may be claimed by more than one wallet. Possession is the entitlement,
// so two uploaders who each genuinely hold the same bytes both get a claim and
// neither blocks the other. Restricting to one would also lock out the ~115k
// legacy track rows whose cid is shared across owners.
func TestProjectContentAttestationCidsAllowsMultipleClaimants(t *testing.T) {
	st := newMemAuthStore()
	ctx := context.Background()

	if err := projectContentAttestationCids(ctx, st, attestationFor(1, "0xfirst", "0xv1", "cid", "320", ""), "tx1"); err != nil {
		t.Fatalf("first projection failed: %v", err)
	}
	if err := projectContentAttestationCids(ctx, st, attestationFor(2, "0xsecond", "0xv2", "cid", "320", ""), "tx1"); err != nil {
		t.Fatalf("second projection failed: %v", err)
	}

	for _, u := range []int64{1, 2} {
		if ok, _ := st.IsCidClaimedByUser(ctx, "cid", u); !ok {
			t.Fatalf("expected user %d to hold a claim", u)
		}
	}
}

// The owner re-uploading their own file is routine and must stay idempotent.
func TestProjectContentAttestationCidsReattestationIsNoop(t *testing.T) {
	st := newMemAuthStore()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := projectContentAttestationCids(ctx, st, attestationFor(1, "0xowner", "0xv1", "cid", "320", ""), "tx1"); err != nil {
			t.Fatalf("re-attestation %d failed: %v", i, err)
		}
	}
	if len(st.cids) != 2 {
		t.Fatalf("re-attesting for the same user must stay one claim per cid, got %d", len(st.cids))
	}
}

// The reported bypass: read a gated track's cid off the public API, then
// assert it on your own track. The decoy track's owner is legitimate, so
// signer authorization passes — content authorization is what has to stop it.
func TestContentAuthRejectsClaimingAnotherUsersCid(t *testing.T) {
	st := newMemAuthStore()
	ctx := context.Background()

	mustProject(t, st, userCreateTx(1, "0xartist", "artist"))
	mustProject(t, st, userCreateTx(2, "0xattacker", "attacker"))
	if err := projectContentAttestationCids(ctx, st, attestation("0xartist", "0xv1", "orig", "gated320", ""), "tx1"); err != nil {
		t.Fatalf("projection failed: %v", err)
	}

	decoy := trackCidTx(2, 2_000_001, "0xattacker", "Create", map[string]any{"track_cid": "gated320"})
	mustRejectContentAuth(t, st, decoy, "was not uploaded for user 2")
}

// A cid nobody has attested cannot be asserted at all.
func TestContentAuthRejectsUnattestedCid(t *testing.T) {
	st := newMemAuthStore()
	mustProject(t, st, userCreateTx(1, "0xartist", "artist"))

	tx := trackCidTx(1, 2_000_001, "0xartist", "Create", map[string]any{"track_cid": "nobody-attested-this"})
	mustRejectContentAuth(t, st, tx, "is not attested to any uploader")
}

// orig_file_cid is the download path — the lossless master — so it is checked
// on the same footing as the streamable transcode.
func TestContentAuthChecksOrigAndPreviewCids(t *testing.T) {
	ctx := context.Background()

	for _, key := range []string{"orig_file_cid", "preview_cid"} {
		st := newMemAuthStore()
		mustProject(t, st, userCreateTx(1, "0xartist", "artist"))
		mustProject(t, st, userCreateTx(2, "0xattacker", "attacker"))
		if err := projectContentAttestationCids(ctx, st, attestationFor(1, "0xartist", "0xv1", "victim-orig", "victim-320", "victim-preview"), "tx1"); err != nil {
			t.Fatalf("projection failed: %v", err)
		}
		// Give the attacker a legitimately attested track_cid so only the
		// field under test can be the reason for rejection.
		if err := projectContentAttestationCids(ctx, st, attestationFor(2, "0xattacker", "0xv1", "own-orig", "own-320", "own-preview"), "tx1"); err != nil {
			t.Fatalf("projection failed: %v", err)
		}

		victim := map[string]string{"orig_file_cid": "victim-orig", "preview_cid": "victim-preview"}
		tx := trackCidTx(2, 2_000_001, "0xattacker", "Create", map[string]any{
			"track_cid": "own-320",
			key:         victim[key],
		})
		mustRejectContentAuth(t, st, tx, key)
	}
}

// The uploader claiming their own content is the ordinary path.
func TestContentAuthAllowsOwnUpload(t *testing.T) {
	st := newMemAuthStore()
	mustProject(t, st, userCreateTx(1, "0xartist", "artist"))
	if err := projectContentAttestationCids(context.Background(), st, attestation("0xartist", "0xv1", "orig", "320", "preview"), "tx1"); err != nil {
		t.Fatalf("projection failed: %v", err)
	}

	mustContentAuth(t, st, trackCidTx(1, 2_000_001, "0xartist", "Create", map[string]any{
		"track_cid":     "320",
		"orig_file_cid": "orig",
		"preview_cid":   "preview",
	}))
}

// A manager acting under an approved grant uploads as themselves but publishes
// for the artist. Raw wallet equality would break that; the check has to go
// through the same signer predicate the rest of the auth state uses.
func TestContentAuthAllowsUploadByApprovedManager(t *testing.T) {
	st := newMemAuthStore()
	ctx := context.Background()

	mustProject(t, st, userCreateTx(1, "0xartist", "artist"))
	mustProject(t, st, userCreateTx(2, "0xmanager", "manager"))
	mustProject(t, st, authTx{
		UserID: 1, EntityType: "Grant", Action: "Create", Signer: "0xartist",
		meta: map[string]any{"grantee_address": "0xmanager"},
	})
	mustProject(t, st, authTx{
		UserID: 1, EntityType: "Grant", Action: "Approve", Signer: "0xartist",
		meta: map[string]any{"grantee_address": "0xmanager", "grantor_user_id": float64(1)},
	})

	// Manager's wallet uploaded the bytes; the track belongs to the artist.
	if err := projectContentAttestationCids(ctx, st, attestation("0xmanager", "0xv1", "orig", "320", ""), "tx1"); err != nil {
		t.Fatalf("projection failed: %v", err)
	}

	mustContentAuth(t, st, trackCidTx(1, 2_000_001, "0xmanager", "Create", map[string]any{"track_cid": "320"}))
}

// Replacing audio is a shipped feature, so an update naming a newly attested
// cid must pass while the previously claimed cid stays claimed.
func TestContentAuthAllowsAudioReplacement(t *testing.T) {
	st := newMemAuthStore()
	ctx := context.Background()

	mustProject(t, st, userCreateTx(1, "0xartist", "artist"))
	if err := projectContentAttestationCids(ctx, st, attestation("0xartist", "0xv1", "orig-v1", "320-v1", ""), "tx1"); err != nil {
		t.Fatalf("projection failed: %v", err)
	}
	if err := projectContentAttestationCids(ctx, st, attestation("0xartist", "0xv1", "orig-v2", "320-v2", ""), "tx1"); err != nil {
		t.Fatalf("projection failed: %v", err)
	}

	mustContentAuth(t, st, trackCidTx(1, 2_000_001, "0xartist", "Update", map[string]any{"track_cid": "320-v2"}))

	// The superseded cid must remain claimed: those bytes are still in storage
	// and still gated, so releasing the claim would let anyone pick it up.
	if ok, _ := st.IsCidClaimedByUser(ctx, "320-v1", 1); !ok {
		t.Fatal("the replaced cid must stay claimed")
	}
}

// Metadata-only edits must not re-validate audio the transaction never names,
// or every legacy track becomes uneditable the moment enforcement activates.
func TestContentAuthIgnoresUpdatesWithoutCids(t *testing.T) {
	st := newMemAuthStore()
	mustProject(t, st, userCreateTx(1, "0xartist", "artist"))

	mustContentAuth(t, st, authTx{
		UserID: 1, EntityType: "Track", EntityID: 2_000_001, Action: "Update", Signer: "0xartist",
		meta: map[string]any{"title": "a new title"},
	})
}

// The genesis replay is the only path allowed to assert a cid without an
// attestation, and it is what makes enforcement activatable over legacy data.
func TestMigrationSeedsCidsAndBypassesEnforcement(t *testing.T) {
	st := newMemAuthStore()
	ctx := context.Background()

	migrated := trackCidTx(1, 500, "0xLegacyOwner", "Create", map[string]any{
		"track_cid":     "legacy-320",
		"orig_file_cid": "legacy-orig",
	})
	migrated.Migration = true

	if err := projectMigratedTrackCids(ctx, st, migrated); err != nil {
		t.Fatalf("migration seeding failed: %v", err)
	}
	if ok, _ := st.IsCidClaimedByUser(ctx, "legacy-320", 1); !ok {
		t.Fatal("expected the legacy cid seeded to its owner")
	}

	// Migration transactions are exempt from the check itself.
	mustContentAuth(t, st, migrated)
}

// Live (non-migration) transactions must never seed their own claims, or the
// bypass returns via a self-asserted cid.
func TestLiveTrackDoesNotSeedCids(t *testing.T) {
	st := newMemAuthStore()
	tx := trackCidTx(1, 2_000_001, "0xattacker", "Create", map[string]any{"track_cid": "someone-elses"})

	if err := projectMigratedTrackCids(context.Background(), st, tx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.cids) != 0 {
		t.Fatalf("live track must not seed cid claims, got %v", st.cids)
	}
}

// Non-track entities carry no audio and must pass through untouched.
func TestContentAuthIgnoresNonTrackEntities(t *testing.T) {
	st := newMemAuthStore()
	mustContentAuth(t, st, authTx{
		UserID: 1, EntityType: "Playlist", EntityID: 400_001, Action: "Create", Signer: "0xw",
		meta: map[string]any{"track_cid": "not-a-track-field"},
	})
}

// Migration Track Create metadata is shaped
// {"access_authorities":[...],"data":{"track_cid":...}} with the owner's wallet
// as signer, matching what cmd/genesis-writer/entities_track.go emits. Nothing
// enforces that shape across the two components, so pin it here.
//
// Note this only covers paths that reach FinalizeBlock — genesis-replay, or a
// migration transaction submitted live. genesis-writer inserts into
// core_blocks/core_transactions directly and never calls the projection at all,
// so it seeds nothing; that gap needs closing separately.
func TestGenesisWriterEnvelopeSeedsCids(t *testing.T) {
	st := newMemAuthStore()
	raw := `{"access_authorities":["0xAA"],"data":{"track_cid":"320cid","preview_cid":"prevcid","orig_file_cid":"origcid","title":"t","owner_id":1}}`

	tx := authTx{
		UserID: 1, EntityType: "Track", EntityID: 500, Action: "Create",
		Signer: "0xOwnerWallet", Migration: true,
		meta: parseAuthMetadata(raw),
	}

	ctx := context.Background()
	if err := projectMigratedTrackCids(ctx, st, tx); err != nil {
		t.Fatalf("seeding failed: %v", err)
	}
	for _, cid := range []string{"320cid", "prevcid", "origcid"} {
		if ok, _ := st.IsCidClaimedByUser(ctx, cid, 1); !ok {
			t.Fatalf("expected %q seeded to the owning user", cid)
		}
	}
}

// The point of allowing several claimants: two users who each genuinely
// uploaded the same bytes can both publish. Under one-wallet-per-cid the second
// would be permanently locked out of a cid they legitimately possess, which is
// also what would happen to the ~115k legacy track rows sharing a cid across
// owners once the migration seeds them.
func TestContentAuthAllowsEitherOfTwoClaimants(t *testing.T) {
	st := newMemAuthStore()
	ctx := context.Background()

	mustProject(t, st, userCreateTx(1, "0xalice", "alice"))
	mustProject(t, st, userCreateTx(2, "0xbob", "bob"))

	// Both uploaded the same bytes, so both hold a claim on the same cid.
	for _, c := range []struct {
		user   int64
		wallet string
	}{{1, "0xalice"}, {2, "0xbob"}} {
		if err := projectContentAttestationCids(ctx, st, attestationFor(c.user, c.wallet, "0xv1", "orig", "shared320", ""), "tx1"); err != nil {
			t.Fatalf("projection for %s failed: %v", c.wallet, err)
		}
	}

	mustContentAuth(t, st, trackCidTx(1, 2_000_001, "0xalice", "Create", map[string]any{"track_cid": "shared320"}))
	mustContentAuth(t, st, trackCidTx(2, 2_000_002, "0xbob", "Create", map[string]any{"track_cid": "shared320"}))

	// A third party with no claim still cannot assert it.
	mustProject(t, st, userCreateTx(3, "0xmallory", "mallory"))
	mustRejectContentAuth(t, st,
		trackCidTx(3, 2_000_003, "0xmallory", "Create", map[string]any{"track_cid": "shared320"}),
		"was not uploaded for user 3")
}

// A developer app is one wallet shared by every user who granted it. If claims
// were keyed on the uploading wallet, any of an app's users could assert any
// other's uploads just by holding a grant to the same app — reopening the
// bypass for the whole population of a popular integration.
func TestContentAuthRejectsSnipeViaSharedDevApp(t *testing.T) {
	st := newMemAuthStore()
	ctx := context.Background()

	mustProject(t, st, userCreateTx(1, "0xuser1", "one"))
	mustProject(t, st, userCreateTx(2, "0xuser2", "two"))
	mustProject(t, st, authTx{UserID: 1, EntityType: "DeveloperApp", Action: "Create", Signer: "0xuser1",
		meta: map[string]any{"address": "0xapp"}})
	// Both users grant the same app.
	mustProject(t, st, authTx{UserID: 1, EntityType: "Grant", Action: "Create", Signer: "0xuser1",
		meta: map[string]any{"grantee_address": "0xapp"}})
	mustProject(t, st, authTx{UserID: 2, EntityType: "Grant", Action: "Create", Signer: "0xuser2",
		meta: map[string]any{"grantee_address": "0xapp"}})

	// The app uploads for user 1.
	if err := projectContentAttestationCids(ctx, st,
		attestationFor(1, "0xapp", "0xv1", "orig", "user1-320", ""), "tx1"); err != nil {
		t.Fatal(err)
	}

	// User 2 shares the app but the upload was not for them.
	mustRejectContentAuth(t, st,
		trackCidTx(2, 2_000_009, "0xuser2", "Create", map[string]any{"track_cid": "user1-320"}),
		"was not uploaded for user 2")

	// User 1 can still publish it, including via the app as signer.
	mustContentAuth(t, st, trackCidTx(1, 2_000_010, "0xuser1", "Create", map[string]any{"track_cid": "user1-320"}))
	mustContentAuth(t, st, trackCidTx(1, 2_000_011, "0xapp", "Create", map[string]any{"track_cid": "user1-320"}))
}

// A preview can be generated long after the upload it belongs to — changing a
// track's preview start regenerates it — so claims must accumulate across
// attestations rather than being fixed at transcode time.
func TestProjectContentAttestationCidsAccumulateAcrossAttestations(t *testing.T) {
	ctx := context.Background()
	st := newMemAuthStore()

	if err := projectContentAttestationCids(ctx, st, attestation("0xup", "0xval", "origcid", "320cid"), "tx1"); err != nil {
		t.Fatal(err)
	}
	tx := trackCidTx(1, 10, "0xup", "Create", map[string]any{"track_cid": "320cid", "preview_cid": "latepreview"})
	mustRejectContentAuth(t, st, tx, "not attested to any uploader")

	if err := projectContentAttestationCids(ctx, st, attestation("0xup", "0xval", "latepreview"), "tx2"); err != nil {
		t.Fatal(err)
	}
	mustContentAuth(t, st, tx)
}
