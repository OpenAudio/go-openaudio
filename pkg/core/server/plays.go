package server

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"
)

const MaxPlaysPerTransaction = 1000

func validatePlayTransactionShape(stx *v1.SignedTransaction) error {
	plays := stx.GetPlays()
	if plays == nil {
		return txRejectedf("play tx has no body")
	}
	if len(plays.GetPlays()) == 0 {
		return txRejectedf("play tx has no plays")
	}
	if len(plays.GetPlays()) > MaxPlaysPerTransaction {
		return txRejectedf("play tx has %d plays, max %d", len(plays.GetPlays()), MaxPlaysPerTransaction)
	}
	for i, play := range plays.GetPlays() {
		if play.GetSignature() == "" {
			return txRejectedf("play %d has no signature", i)
		}
	}
	if stx.GetSignature() == "" {
		return txRejectedf("play tx has no signature")
	}
	return nil
}

func recoverPlaysSigner(signature string, plays *v1.TrackPlays) (string, error) {
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, "0x"))
	if err != nil {
		return "", txRejectedf("play tx signature is not hex: %v", err)
	}
	if len(sig) != crypto.SignatureLength {
		return "", txRejectedf("play tx signature is %d bytes, want %d", len(sig), crypto.SignatureLength)
	}
	body, err := proto.Marshal(plays)
	if err != nil {
		return "", txRejectedf("marshal plays: %v", err)
	}
	hash := accounts.TextHash(crypto.Keccak256(body))
	pub, err := crypto.SigToPub(hash, sig)
	if err != nil {
		return "", txRejectedf("recover play tx signer: %v", err)
	}
	return crypto.PubkeyToAddress(*pub).Hex(), nil
}

func (s *Server) isValidPlayTransaction(ctx context.Context, stx *v1.SignedTransaction) error {
	if err := validatePlayTransactionShape(stx); err != nil {
		return err
	}
	signer, err := recoverPlaysSigner(stx.GetSignature(), stx.GetPlays())
	if err != nil {
		return err
	}
	registered, err := s.db.IsRegisteredNodeEthAddress(ctx, signer)
	if err != nil {
		return fmt.Errorf("look up play tx signer: %w", err)
	}
	if !registered {
		return txRejectedf("play tx signer %s is not a registered node", signer)
	}
	return nil
}

func (s *Server) finalizePlayTransaction(_ context.Context, stx *v1.SignedTransaction) (*v1.TrackPlays, error) {
	tx := stx.GetPlays()
	if tx == nil {
		return nil, errors.New("invalid play tx")
	}

	return tx, nil
}
