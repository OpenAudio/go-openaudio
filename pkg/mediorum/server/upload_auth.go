package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	coreServer "github.com/OpenAudio/go-openaudio/pkg/core/server"
	"go.uber.org/zap"
)

// Upload attribution and the on-chain attestation that follows it.
//
// Consensus cannot tell a genuine cid claim from a stolen one (see
// pkg/core/server/content_auth_state.go); it needs a statement from the node
// that witnessed the bytes. The uploading user is deliberately taken as an
// unauthenticated assertion — a userId in tus metadata — rather than proven
// with a request signature, because the assertion is not what makes claims
// safe:
//
//   - A claim cannot be stolen by asserting someone else's id: asserting user
//     X only ever credits X. Exercising the claim — naming the cid on a track —
//     happens in an entity-manager write, which is signed and grant-checked.
//     That write is where the user actually authenticates.
//   - A claim cannot cover content the caller does not hold: it is derived
//     from bytes this node received, so producing a gated track's cid requires
//     the gated bytes themselves.
//
// The corollary, and the standing constraint on anything built on this state:
// CLAIMS ARE AUTHORIZATION MATERIAL, NOT USER ACTIVITY. A claim records that
// somebody asserted this user when bytes were uploaded, and nothing more. No
// feature may treat "user X claims cid C" as evidence that X did anything —
// no attribution UIs, no quotas, no provenance — without adding real
// authentication first.

// contentAuthEnabled reports whether this node requires upload attribution and
// attests cids. Deliberately independent of ProgrammableDistributionEnabled —
// see IsContentAuthEnabled.
func (ss *MediorumServer) contentAuthEnabled() bool {
	return ss.Config.ContentAuthEnabled
}

// resolveUploadUserID reads the asserted uploading user from tus metadata.
//
// Only audio carries an attribution requirement: images are served
// unauthenticated so there is nothing to claim, and requiring attribution on
// image uploads would break signup, which uploads a profile picture before the
// account's user id exists.
//
// A missing userId on audio is rejected when content auth is enabled — the
// upload could never earn an attestation, so failing at create saves the
// client sending bytes it can never use, and surfaces the misconfiguration at
// the call site instead of at publish. A malformed userId is rejected
// regardless: a bad assertion must not masquerade as no assertion.
func (ss *MediorumServer) resolveUploadUserID(template JobTemplate, metadata map[string]string) (int64, error) {
	if template != JobTemplateAudio {
		return 0, nil
	}

	raw, ok := metadata["userId"]
	if !ok || raw == "" {
		if ss.contentAuthEnabled() {
			return 0, errors.New("audio uploads must carry the uploading user's id")
		}
		return 0, nil
	}

	userID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || userID <= 0 {
		return 0, fmt.Errorf("upload carries an unusable user id %q", raw)
	}
	return userID, nil
}

// contentAttestationFor builds the unsigned attestation these cids warrant, or
// nil when this upload can never earn one: content auth is off, there is
// nothing to attest, or no user was asserted.
//
// Split from sending so the "should we attest?" rules are testable without a
// chain, and so callers can tell "nothing to wait for" from "the send failed".
//
// UserWallet is deliberately not consulted: the legacy POST /uploads and gRPC
// paths populate it from an unverified X-User-Wallet-Addr header, and those
// uploads carry no user id, so they never reach an attestation. Claims key on
// the user id alone (see content_auth_state.go on why not the wallet).
func (ss *MediorumServer) contentAttestationFor(upload *Upload, cids []string) *v1.ContentAttestation {
	if !ss.contentAuthEnabled() {
		return nil
	}

	present := make([]string, 0, len(cids))
	for _, cid := range cids {
		if cid != "" {
			present = append(present, cid)
		}
	}
	if len(present) == 0 {
		return nil
	}

	// The claim is keyed on the user the upload was made for, so an attestation
	// without one authorizes nobody.
	if !upload.UserID.Valid || upload.UserID.Int64 == 0 {
		ss.logger.Debug("skipping attestation: upload has no user id", zap.String("uploadID", upload.ID))
		return nil
	}

	return &v1.ContentAttestation{
		UserId:           upload.UserID.Int64,
		Cids:             present,
		ValidatorAddress: ss.Config.Self.Wallet,
	}
}

// attestUploadCids tells the chain that these bytes were uploaded here for a
// user, which is what entitles that user to name the cids on a track.
//
// Takes the cids explicitly rather than reading them off the upload because
// they do not all exist at the same moment: the original and the transcode are
// known when transcoding finishes, but a preview can be generated much later,
// and changing a track's preview start produces a new one every time. Callers
// attest each cid as it comes into being.
//
// Blocks until the transaction is committed, and callers must not report the
// cids as ready before it returns: a client creates its track the moment an
// upload reads done, and consensus rejects a track naming cids it has no claim
// for. An error means these cids are not yet claimable.
func (ss *MediorumServer) attestUploadCids(ctx context.Context, upload *Upload, cids ...string) error {
	ca := ss.contentAttestationFor(upload, cids)
	if ca == nil {
		return nil
	}
	if err := ss.sendContentAttestation(ctx, ca); err != nil {
		return err
	}
	ss.logger.Info("attested upload cids",
		zap.String("uploadID", upload.ID),
		zap.Int64("userID", ca.UserId),
		zap.Strings("cids", ca.Cids))
	return nil
}

// contentAttestation builds an attestation for a single cid this node
// produced, e.g. a preview sliced from claimed source audio. The validator
// signature added at send time is the only load-bearing one.
func contentAttestation(userID int64, cid, validatorAddress string) *v1.ContentAttestation {
	return &v1.ContentAttestation{
		UserId:           userID,
		Cids:             []string{cid},
		ValidatorAddress: validatorAddress,
	}
}

// sendContentAttestation signs and submits, blocking until the transaction is
// committed so callers can treat a nil return as "these cids are claimable".
func (ss *MediorumServer) sendContentAttestation(ctx context.Context, ca *v1.ContentAttestation) error {
	if ss.core == nil {
		return nil
	}

	// Shared constructor, so signer and verifier cannot drift on field order or
	// address casing.
	payload, err := coreServer.ContentAttestationBytes(ca)
	if err != nil {
		return fmt.Errorf("could not marshal content attestation: %w", err)
	}
	sig, err := common.EthSign(ss.Config.privateKey, payload)
	if err != nil {
		return fmt.Errorf("could not sign content attestation: %w", err)
	}
	ca.ValidatorSignature = sig

	if _, err := ss.core.SendTransaction(ctx, &connect.Request[v1.SendTransactionRequest]{
		Msg: &v1.SendTransactionRequest{
			Transaction: &v1.SignedTransaction{
				Transaction: &v1.SignedTransaction_ContentAttestation{ContentAttestation: ca},
			},
		},
	}); err != nil {
		return fmt.Errorf("could not send content attestation for %v: %w", ca.Cids, err)
	}
	return nil
}

// nullInt64 is a small helper for the optional user id column.
func nullInt64(v int64) sql.NullInt64 {
	if v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}
