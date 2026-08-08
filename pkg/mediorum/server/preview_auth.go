package server

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
)

// Authorization for previews generated from a bare cid.
//
// POST /generate_preview takes a cid and an offset and produces new bytes with
// nothing to credit them to. It never consults an upload record, and often
// there is none to consult — the source may be a legacy Qm cid, which is why
// repair scrolls qm_cids separately from uploads.
//
// The result still needs a claim: a preview cid that anyone could name on
// their own track would let an attacker tile a gated track at 30-second
// offsets and reassemble it. The claim goes to the asserted user, and only if
// that user already claims the source — crediting a user who does not hold
// the source is the whole attack; crediting one who does grants nothing they
// did not already hold.
//
// The requester is deliberately not authenticated. Asserting the owner's id
// gets a preview attested to the owner, which the requester cannot use: the
// preview cid streams only through signed, track-scoped URLs (see serveBlob —
// audio is never served by bare cid), a streaming signature is only issued
// for cids resolved from a track record, and naming the cid on a track
// requires a signed entity-manager write backed by the claim. That serving
// invariant is load-bearing for gated content generally, not just previews:
// if audio ever becomes fetchable by bare cid, gated tracks leak directly,
// previews or not.

// errPreviewUnverifiable means the node could not reach the state it needs to
// authorize, as opposed to the caller getting it wrong. The handler turns this
// into a 503 rather than a 401 so clients retry instead of re-asserting.
var errPreviewUnverifiable = errors.New("cannot authorize a preview while core is unavailable")

// previewClaimant confirms the asserted user may claim the source cid,
// returning the user id to attest the preview to.
//
// Returns 0 with no error when content auth is off, meaning generate a preview
// as before and attest nothing.
func (ss *MediorumServer) previewClaimant(ctx context.Context, sourceCID string, userID int64) (int64, error) {
	if !ss.contentAuthEnabled() {
		return 0, nil
	}

	// Checked before core availability: a missing user id is the caller's
	// error whatever state this node is in, and saying so is both more useful
	// and cheaper than reporting an outage.
	if userID == 0 {
		return 0, errors.New("preview requests must carry the requesting user's id")
	}

	if ss.core == nil {
		return 0, errPreviewUnverifiable
	}

	// The source claim is what makes this safe. Without it the endpoint would
	// mint a claim over a 30-second window of anyone's audio.
	claimed, err := ss.core.IsCidClaimedByUser(ctx, sourceCID, userID)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errPreviewUnverifiable, err)
	}
	if !claimed {
		ss.logger.Info("refusing preview of unclaimed source",
			zap.String("sourceCID", sourceCID), zap.Int64("userID", userID))
		return 0, fmt.Errorf("user %d may not claim %s", userID, sourceCID)
	}

	return userID, nil
}

// attestPreviewCid records the preview as belonging to the user that claims
// the source. Blocks until the transaction commits: the caller writes this cid
// straight onto a track, and consensus rejects a track naming a cid it has no
// claim for.
func (ss *MediorumServer) attestPreviewCid(ctx context.Context, userID int64, previewCID string) error {
	if userID == 0 || previewCID == "" || ss.core == nil {
		return nil
	}
	return ss.sendContentAttestation(ctx, contentAttestation(userID, previewCID, ss.Config.Self.Wallet))
}
