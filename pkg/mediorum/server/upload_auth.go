package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	coreServer "github.com/OpenAudio/go-openaudio/pkg/core/server"
	"go.uber.org/zap"
)

// Upload authentication and the on-chain attestation that follows it.
//
// Consensus cannot tell a genuine cid claim from a stolen one (see
// pkg/core/server/content_auth_state.go); it needs a statement from the node
// that witnessed the bytes. That is only worth anything if the node knows who
// uploaded them, which is what the signature here establishes.

// uploadSignatureMaxAge bounds replay of a captured upload signature. Uploads
// begin immediately after signing, so this only has to cover clock skew and a
// slow client, not a whole session.
const uploadSignatureMaxAge = 10 * time.Minute

// tusUploaderIdentity is what a verified upload signature establishes.
type tusUploaderIdentity struct {
	Wallet string
	UserID int64
	// Signature is the envelope as presented, kept for dispute forensics.
	Signature string
}

// contentAuthEnabled reports whether this node verifies upload signatures and
// attests cids. Deliberately independent of ProgrammableDistributionEnabled —
// see IsContentAuthEnabled.
func (ss *MediorumServer) contentAuthEnabled() bool {
	return ss.Config.ContentAuthEnabled
}

// verifyUploadSignature recovers the uploader from tus metadata. The client
// signs EIP-712 typed data; see upload_request_eip712.go for why.
//
// The payload covers userId and timestamp, not the cid — at tus-create time the
// bytes have not been sent and nobody knows it yet. Binding to content happens
// when this node attests to the cids it produced.
func verifyUploadSignature(metadata map[string]string) (tusUploaderIdentity, error) {
	sig := metadata["signature"]
	if sig == "" {
		return tusUploaderIdentity{}, errors.New("upload signature missing")
	}

	userID, err := strconv.ParseInt(metadata["userId"], 10, 64)
	if err != nil || userID == 0 {
		return tusUploaderIdentity{}, errors.New("upload signature carries no usable user id")
	}
	timestamp, err := strconv.ParseInt(metadata["timestamp"], 10, 64)
	if err != nil {
		return tusUploaderIdentity{}, errors.New("upload is missing a signed timestamp")
	}

	age := time.Since(time.UnixMilli(timestamp))
	if age > uploadSignatureMaxAge {
		return tusUploaderIdentity{}, fmt.Errorf("upload signature too old: %s", age)
	}
	// A future-dated timestamp yields a negative age, which would otherwise
	// sail past the check above and never expire.
	if age < -uploadSignatureMaxAge {
		return tusUploaderIdentity{}, fmt.Errorf("upload signature timestamp is in the future: %s", -age)
	}

	// Both fields travel unsigned in tus metadata but are reproduced inside the
	// typed data, so tampering with either changes the digest and recovery no
	// longer yields the signer.
	wallet, err := coreServer.RecoverUploadRequestSigner(userID, timestamp, sig)
	if err != nil {
		return tusUploaderIdentity{}, fmt.Errorf("upload signature invalid: %w", err)
	}

	return tusUploaderIdentity{
		Wallet:    strings.ToLower(wallet),
		UserID:    userID,
		Signature: sig,
	}, nil
}

// resolveUploadIdentity verifies the signature when one is present, and
// reports whether the upload may proceed.
//
// An unsigned upload is allowed through unless verification is required, but
// it is never credited to a wallet: a wallet taken from unsigned metadata is
// an assertion by whoever made the request, and writing it down would put a
// forgeable value in the field attestations are built from.
func (ss *MediorumServer) resolveUploadIdentity(template JobTemplate, metadata map[string]string) (tusUploaderIdentity, error) {
	// Only audio carries the gating risk this exists to close, and images are
	// served unauthenticated anyway. Requiring signatures on image uploads
	// would break signup, which uploads a profile picture before the account's
	// user id exists.
	if template != JobTemplateAudio {
		return tusUploaderIdentity{}, nil
	}

	if metadata["signature"] == "" {
		if ss.contentAuthEnabled() {
			return tusUploaderIdentity{}, errors.New("audio uploads must be signed")
		}
		return tusUploaderIdentity{}, nil
	}

	identity, err := verifyUploadSignature(metadata)
	if err != nil {
		// A signature that was offered and does not verify is always an error,
		// whether or not signing is mandatory here. Falling back to anonymous
		// would let a bad signature masquerade as no signature.
		return tusUploaderIdentity{}, err
	}
	return identity, nil
}

// contentAttestationFor builds the unsigned attestation these cids warrant, or
// nil when this upload can never earn one: content auth is off, there is
// nothing to attest, or there is no verified signer to credit.
//
// Split from sending so the "should we attest?" rules are testable without a
// chain, and so callers can tell "nothing to wait for" from "the send failed".
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

	// Require a verified signature, not merely a wallet: the legacy
	// POST /uploads and gRPC paths still populate UserWallet from an unverified
	// X-User-Wallet-Addr header, so a forged header could otherwise mint a
	// claim over someone else's content.
	if !upload.UploadSignature.Valid || upload.UploadSignature.String == "" {
		ss.logger.Debug("skipping attestation for unsigned upload", zap.String("uploadID", upload.ID))
		return nil
	}
	if !upload.UserWallet.Valid || upload.UserWallet.String == "" {
		ss.logger.Debug("skipping attestation for upload with no wallet", zap.String("uploadID", upload.ID))
		return nil
	}
	// The claim is keyed on the user the upload was made for, so an attestation
	// without one authorizes nobody.
	if !upload.UserID.Valid || upload.UserID.Int64 == 0 {
		ss.logger.Warn("skipping attestation: upload has no user id", zap.String("uploadID", upload.ID))
		return nil
	}

	return &v1.ContentAttestation{
		UploaderAddress:   strings.ToLower(upload.UserWallet.String),
		UserId:    upload.UserID.Int64,
		Cids:              present,
		ValidatorAddress:  ss.Config.Self.Wallet,
		UploaderSignature: upload.UploadSignature.String,
	}
}

// attestUploadCids tells the chain that a user uploaded these bytes here, which
// is what entitles them to name the cids on a track.
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
	if ca == nil || ss.core == nil {
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

	ss.logger.Info("attested upload cids",
		zap.String("uploadID", upload.ID),
		zap.String("uploader", ca.UploaderAddress),
		zap.Int64("userID", ca.UserId),
		zap.Strings("cids", ca.Cids))
	return nil
}

// nullString is a small helper for the optional identity columns.
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// nullInt64 mirrors nullString for the user id.
func nullInt64(v int64) sql.NullInt64 {
	if v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}
