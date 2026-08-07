package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
)

// Height-gated authorization enforcement for ManageEntity transactions
// (Rules.AuthEnforced). Validation is a dry run of the same projection
// FinalizeBlock applies (auth_state.go), pointed at a throwaway
// overlayAuthStore (auth_store_overlay.go) instead of the block's
// transaction. Sharing the code path is what keeps proposal-time validation
// and finalize-time state transition from ever disagreeing; the overlay is
// what lets a transaction depend on an earlier transaction in the same
// proposal (create user, then create track).

// validateManageEntityAuth is the enforcement check: the signer field must be
// exactly the EIP-712 recovered address (an injected Signer is otherwise just
// a claim — any peer could forward a transaction naming someone else's
// wallet), and the transaction's auth effects must apply cleanly against the
// overlay. On success the effects are retained in the overlay so later
// transactions in the same proposal can build on them. Returns nil for
// transactions the projection is not tracking; errors are either a
// deterministic rejection or a store failure, which the caller distinguishes
// with authRejected.
func (s *Server) validateManageEntityAuth(ctx context.Context, rules config.Rules, overlay authStore, em *v1.ManageEntityLegacy) error {
	recovered, _, err := RecoverPubkeyFromCoreTx(s.config, em)
	if err != nil {
		return &authRejectionError{reason: fmt.Sprintf("signer not recoverable: %v", err)}
	}
	if !strings.EqualFold(recovered, em.GetSigner()) {
		return &authRejectionError{reason: fmt.Sprintf("signer %s does not match recovered address %s", em.GetSigner(), recovered)}
	}

	tx := authTxFromManageEntity(em)

	// Content authorization runs before the projection so a track asserting a
	// cid it has no claim to is rejected without leaving the entity behind in
	// the overlay for later transactions in the proposal to build on.
	if rules.ContentAuthEnforced {
		if err := validateTrackContentAuth(ctx, overlay, tx); err != nil {
			if isAuthValidationError(err) {
				return &authRejectionError{reason: err.Error()}
			}
			return err
		}
	}

	err = applyAuthProjection(ctx, overlay, tx)
	if err == nil {
		return nil
	}
	if isAuthValidationError(err) {
		return &authRejectionError{reason: err.Error()}
	}
	return err
}

// authRejectionError is a deterministic authorization failure, as opposed to
// a store error that means "this node cannot tell". ProcessProposal must
// reject on the former and report unknown on the latter.
type authRejectionError struct {
	reason string
}

func (e *authRejectionError) Error() string {
	return "manage entity rejected: " + e.reason
}

// txRejectionError is the same distinction for transaction types outside the
// manage-entity path: a deterministic reason to vote a block invalid, as
// opposed to a store failure meaning "this node cannot tell". authRejected
// matches both, so ProcessProposal can treat them uniformly.
type txRejectionError struct {
	reason string
}

func (e *txRejectionError) Error() string {
	return e.reason
}

func txRejectedf(format string, args ...any) error {
	return &txRejectionError{reason: fmt.Sprintf(format, args...)}
}

// authRejected reports whether err is (or wraps) a deterministic rejection.
func authRejected(err error) bool {
	var r *authRejectionError
	if errors.As(err, &r) {
		return true
	}
	var t *txRejectionError
	return errors.As(err, &t)
}
