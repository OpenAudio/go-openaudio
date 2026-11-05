package etl

import (
	"context"
	"errors"
	"fmt"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/etl/processors"
)

// knownNodeLookup reports whether address belongs to a validator node that
// has ever been registered.
type knownNodeLookup func(ctx context.Context, address string) (bool, error)

// playsRejection returns why a Plays transaction at height must not be
// indexed, or "" when it may be. A non-nil error means the signer could not be
// looked up and the transaction's fate is unknown.
func (e *Indexer) playsRejection(ctx context.Context, stx *corev1.SignedTransaction, height int64, isKnown knownNodeLookup) (string, error) {
	if !e.config.VerifyPlaySigners || height <= e.config.GenesisMigrationEndHeight {
		return "", nil
	}
	signer, err := processors.RecoverPlaysSigner(stx.GetSignature(), stx.GetPlays())
	if errors.Is(err, processors.ErrPlaysUnsigned) {
		return "unsigned", nil
	}
	if err != nil {
		return err.Error(), nil
	}
	known, err := isKnown(ctx, signer)
	if err != nil {
		return "", fmt.Errorf("look up plays signer %s: %w", signer, err)
	}
	if !known {
		return fmt.Sprintf("signer %s is not a registered node", signer), nil
	}
	return "", nil
}
