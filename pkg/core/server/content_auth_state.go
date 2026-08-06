package server

import (
	"context"
	"strings"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
)

// Consensus-side content authorization (core_auth_cids): which wallet is
// entitled to assert a given cid as a track's audio.
//
// The problem this closes: a track's cid fields are plain client-supplied
// metadata, and nothing ever checked that the writer had any relationship to
// the bytes. Anyone could read a gated track's track_cid off the public API,
// create their own ungated track asserting that same cid, and stream it — the
// access check passes because they genuinely own the decoy track, and the
// storage node only ever verifies "this signature is for this cid", never
// "this cid belongs to that track".
//
// The fix is to make possession the entitlement. A validator that received
// bytes from a wallet attests to that fact on chain (FileUpload), and a track
// may only assert cids whose attestation authorizes the acting user. Forging a
// claim then requires possessing the audio, at which point the paywall was
// already moot — the threat model closes exactly.
//
// Note this deliberately does not require cids to be globally unique. Two
// uploaders who legitimately hold the same bytes each get their own
// attestation and neither blocks the other; uniqueness would break honest
// re-uploads and hand attackers a squatting vector.

// authCidRow is the projection's view of one content claim. The cid itself is
// the lookup key rather than a field, matching authUserRow and authEntityRow.
type authCidRow struct {
	// Wallet entitled to assert this cid on a track.
	UploaderAddress string
	// Validator that witnessed the upload. Empty for migration-seeded claims.
	AttestedBy string
}

// trackCidMetadataKeys are the track metadata fields that name audio content.
// Every one of them is a streamable or downloadable handle, so every one of
// them has to be authorized — orig_file_cid in particular is the download
// path, i.e. the lossless master, which makes it the more valuable leak of the
// three.
var trackCidMetadataKeys = []string{"track_cid", "preview_cid", "orig_file_cid"}

// projectContentAttestationCids records the cids an attestation covers. Called
// from FinalizeBlock after the transaction has been validated, so the
// signature and the attesting validator's registration are already checked.
//
// A cid already claimed by a different wallet is left alone (see the migration
// for why first-attestation-wins), and re-attesting a cid to the wallet that
// already holds it is a no-op rather than an error, since honest retries and
// re-uploads of the same file by the same owner are routine.
func projectContentAttestationCids(ctx context.Context, st authStore, ca *v1.ContentAttestation, blockHeight int64) error {
	if ca == nil {
		return nil
	}
	uploader := strings.ToLower(ca.GetUploaderAddress())
	if uploader == "" {
		return authValidationErrorf("content attestation has no uploader address")
	}
	attestedBy := strings.ToLower(ca.GetValidatorAddress())

	for _, cid := range []string{ca.GetOrigCid(), ca.GetTranscodedCid(), ca.GetPreviewCid()} {
		if cid == "" {
			continue
		}
		if err := st.InsertCid(ctx, cid, uploader, attestedBy, blockHeight); err != nil {
			return err
		}
	}
	return nil
}

// projectMigratedTrackCids seeds claims for a track replayed by the genesis
// migration. The legacy source data is the authority on who owns what, so the
// track's own owner is recorded as the uploader without an attestation — there
// is no upload event to attest to, the bytes were uploaded to the old network
// years ago.
//
// This is what makes enforcement activatable at all. Without it every legacy
// track's cids would be unknown to the projection, and the first edit to any
// of the ~1.4M existing tracks would be rejected.
func projectMigratedTrackCids(ctx context.Context, st authStore, tx authTx) error {
	if !tx.Migration || tx.EntityType != authEntityTypeTrack {
		return nil
	}
	wallet := strings.ToLower(tx.Signer)
	if wallet == "" {
		// A migration row with no signer cannot attribute its cids. Skip
		// rather than fail: the ManageEntity projection already treats an
		// unattributable row as a skip, and failing here would abort a replay.
		return nil
	}
	for _, key := range trackCidMetadataKeys {
		cid := tx.metaString(key)
		if cid == "" {
			continue
		}
		if err := st.InsertCid(ctx, cid, wallet, "", 0); err != nil {
			return err
		}
	}
	return nil
}

// validateTrackContentAuth is the enforcement check, active only under
// Rules.ContentAuthEnforced. Every cid the transaction asserts must be
// recorded, and the wallet that holds it must be authorized to act for the
// user writing the track.
//
// Only cids actually present in this transaction's metadata are checked. An
// update that does not restate a cid does not re-validate it, so ordinary
// metadata edits on a track whose audio predates the projection keep working,
// and replacing a track's audio is checked against the new cid only.
func validateTrackContentAuth(ctx context.Context, st authReader, tx authTx) error {
	if tx.Migration || tx.EntityType != authEntityTypeTrack {
		return nil
	}
	switch tx.Action {
	case authActionCreate, authActionUpdate:
	default:
		return nil
	}

	for _, key := range trackCidMetadataKeys {
		cid := tx.metaString(key)
		if cid == "" {
			continue
		}
		row, ok, err := st.GetCid(ctx, cid)
		if err != nil {
			return err
		}
		if !ok {
			return authValidationErrorf("%s %q is not attested to any uploader", key, cid)
		}
		if err := validateAuthSigner(ctx, st, tx.UserID, row.UploaderAddress); err != nil {
			return authValidationErrorf("%s %q belongs to %s, which is not authorized for user %d",
				key, cid, row.UploaderAddress, tx.UserID)
		}
	}
	return nil
}
