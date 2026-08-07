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
// The result still needs a claim: previews stream publicly, so a preview cid
// that anyone could name on their own track would let an attacker tile a gated
// track at 30-second offsets and reassemble it.
//
// The claim therefore goes to whoever can already claim the source. The caller
// signs so the node knows who is asking, and the request is refused unless that
// user already claims the source cid. Crediting the caller without that check
// is the whole attack; crediting them with it grants nothing they did not
// already hold.

// errPreviewUnverifiable means the node could not reach the state it needs to
// authorize, as opposed to the caller getting it wrong. The handler turns this
// into a 503 rather than a 401 so clients retry instead of re-signing.
var errPreviewUnverifiable = errors.New("cannot authorize a preview while core is unavailable")

// previewClaimant verifies the caller and confirms they may claim the source
// cid, returning the user id to attest the preview to.
//
// Returns 0 with no error when content auth is off, meaning generate a preview
// as before and attest nothing.
func (ss *MediorumServer) previewClaimant(ctx context.Context, sourceCID string, metadata map[string]string) (int64, error) {
	if !ss.contentAuthEnabled() {
		return 0, nil
	}

	// Checked before core availability: a bad signature is the caller's error
	// whatever state this node is in, and saying so is both more useful and
	// cheaper than reporting an outage.
	identity, err := verifyUploadSignature(metadata)
	if err != nil {
		return 0, fmt.Errorf("preview requests must be signed: %w", err)
	}

	if ss.core == nil {
		return 0, errPreviewUnverifiable
	}

	// The source claim is what makes this safe. Without it the endpoint would
	// mint a claim over a 30-second window of anyone's audio.
	claimed, err := ss.core.IsCidClaimedByUser(ctx, sourceCID, identity.UserID)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errPreviewUnverifiable, err)
	}
	if !claimed {
		ss.logger.Info("refusing preview of unclaimed source",
			zap.String("sourceCID", sourceCID), zap.Int64("userID", identity.UserID))
		return 0, fmt.Errorf("user %d may not claim %s", identity.UserID, sourceCID)
	}

	return identity.UserID, nil
}

// attestPreviewCid records the preview as belonging to the user that owns the
// source. Blocks until the transaction commits: the caller writes this cid
// straight onto a track, and consensus rejects a track naming a cid it has no
// claim for.
func (ss *MediorumServer) attestPreviewCid(ctx context.Context, userID int64, previewCID string) error {
	if userID == 0 || previewCID == "" || ss.core == nil {
		return nil
	}
	return ss.sendContentAttestation(ctx, contentAttestation(userID, previewCID, ss.Config.Self.Wallet))
}
