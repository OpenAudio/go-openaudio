package server

import (
	"context"
	"strings"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
)

// Consensus-side content authorization (core_auth_cids): which wallet may
// assert a given cid as a track's audio.
//
// The hole this closes: cid fields are client metadata that nothing checked
// against the bytes. Anyone could read a gated track's track_cid off the public
// API, assert it on their own ungated track, and stream it — the access check
// passes because they own the decoy, and the storage node only verifies "this
// signature is for this cid", never "this cid belongs to that track".
//
// Possession is the entitlement instead: a validator attests that a wallet
// uploaded the bytes, and a track may only name cids whose claim authorizes the
// acting user. Forging one then requires having the audio, at which point the
// paywall was already moot.
//
// A cid may be claimed by several users. Duplicate uploads of the same bytes
// are routine, and there is nothing to arbitrate between two users who both
// genuinely hold them: the check is possession, and both possess. Making the
// claim exclusive would only mean the second honest uploader of a common file
// could not use it.

// trackCidMetadataKeys are the track metadata fields naming audio. Each is a
// streamable or downloadable handle so each needs authorizing; orig_file_cid is
// the download path — the lossless master, and the most valuable of the three.
var trackCidMetadataKeys = []string{"track_cid", "preview_cid", "orig_file_cid"}

// projectContentAttestationCids records the cids an attestation covers. Called
// from FinalizeBlock post-validation, so the signature and the validator's
// registration are already checked. Re-attesting a cid the user already holds
// is a no-op.
func projectContentAttestationCids(ctx context.Context, st authStore, ca *v1.ContentAttestation, txHash string) error {
	if ca == nil {
		return nil
	}
	// A zero user id would authorize nobody. isValidContentAttestation rejects
	// it before finalize reaches here, so this only guards direct callers.
	userID := ca.GetUserId()
	if userID == 0 {
		return authValidationErrorf("content attestation has no user id")
	}

	for _, cid := range ca.GetCids() {
		if cid == "" {
			continue
		}
		if err := st.InsertCid(ctx, cid, userID, txHash); err != nil {
			return err
		}
	}
	return nil
}

// projectMigratedTrackCids seeds claims for a track replayed by the genesis
// migration, recording the owner as uploader with no attestation — the bytes
// went to the old network years ago and the legacy data is the authority.
//
// This is what makes enforcement activatable: without it no legacy cid is
// known, and the first edit to any existing track would be rejected.
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
		// Seeded rows carry no tx hash; see the migration.
		if err := st.InsertCid(ctx, cid, tx.UserID, ""); err != nil {
			return err
		}
	}
	return nil
}

// validateTrackContentAuth is the enforcement check, active only under
// Rules.ContentAuthEnforced: every cid the transaction asserts must be recorded
// to a wallet authorized to act for the writing user.
//
// Only cids present in this transaction are checked, so metadata-only edits on
// a track whose audio predates the projection keep working, and an audio
// replacement is checked against the new cid alone.
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

		// The claim is keyed on the user the upload was made for, not the
		// wallet that performed it. A developer app is one wallet shared by
		// every user who granted it, so asking "can this wallet act for the
		// track's user" would let any of an app's users claim any other's
		// uploads.
		claimed, err := st.IsCidClaimedByUser(ctx, cid, tx.UserID)
		if err != nil {
			return err
		}
		if claimed {
			continue
		}

		known, err := st.CidIsClaimed(ctx, cid)
		if err != nil {
			return err
		}
		if !known {
			return authValidationErrorf("%s %q is not attested to any uploader", key, cid)
		}
		return authValidationErrorf("%s %q was not uploaded for user %d", key, cid, tx.UserID)
	}
	return nil
}
