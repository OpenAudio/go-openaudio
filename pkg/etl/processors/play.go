package processors

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"
)

// ErrPlaysUnsigned is returned by RecoverPlaysSigner for a Plays transaction
// whose envelope carries no signature.
var ErrPlaysUnsigned = errors.New("plays transaction has no signature")

type playProcessor struct{}

func (p *playProcessor) TxType() string { return TxTypePlay }

func (p *playProcessor) Process(ctx context.Context, tx *corev1.SignedTransaction, txCtx *TxContext, q *db.Queries) (*Result, error) {
	plays := tx.GetPlays().GetPlays()
	txCtx.InsertTx.TxType = TxTypePlay
	if len(plays) == 0 {
		return &Result{InsertTx: txCtx.InsertTx}, nil
	}

	userIDs := make([]string, len(plays))
	trackIDs := make([]string, len(plays))
	cities := make([]string, len(plays))
	regions := make([]string, len(plays))
	countries := make([]string, len(plays))
	playedAts := make([]pgtype.Timestamp, len(plays))
	blockHeights := make([]int64, len(plays))
	txHashes := make([]string, len(plays))
	listenedAts := make([]pgtype.Timestamp, len(plays))
	recordedAts := make([]pgtype.Timestamp, len(plays))

	for i, play := range plays {
		userIDs[i] = play.UserId
		trackIDs[i] = play.TrackId
		cities[i] = play.City
		regions[i] = play.Region
		countries[i] = play.Country
		playedAts[i] = pgtype.Timestamp{Time: play.Timestamp.AsTime(), Valid: true}
		blockHeights[i] = txCtx.Block.Height
		txHashes[i] = txCtx.TxHash
		listenedAts[i] = pgtype.Timestamp{Time: play.Timestamp.AsTime(), Valid: true}
		recordedAts[i] = txCtx.BlockTime
	}

	if err := q.InsertPlays(ctx, db.InsertPlaysParams{
		Column1:  userIDs,
		Column2:  trackIDs,
		Column3:  cities,
		Column4:  regions,
		Column5:  countries,
		Column6:  playedAts,
		Column7:  blockHeights,
		Column8:  txHashes,
		Column9:  listenedAts,
		Column10: recordedAts,
	}); err != nil {
		return nil, err
	}
	txCtx.InsertTx.Address = pgtype.Text{String: plays[0].UserId, Valid: true}
	return &Result{InsertTx: txCtx.InsertTx}, nil
}

// RecoverPlaysSigner returns the lowercase 0x address that signed plays,
// using the scheme validator nodes sign Plays transactions with: keccak256 of
// the marshaled TrackPlays, wrapped in the Ethereum signed-message prefix.
func RecoverPlaysSigner(signature string, plays *corev1.TrackPlays) (string, error) {
	if signature == "" {
		return "", ErrPlaysUnsigned
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, "0x"))
	if err != nil {
		return "", fmt.Errorf("decode plays signature: %w", err)
	}
	body, err := proto.Marshal(plays)
	if err != nil {
		return "", fmt.Errorf("marshal plays: %w", err)
	}
	pub, err := crypto.SigToPub(accounts.TextHash(crypto.Keccak256Hash(body).Bytes()), sig)
	if err != nil {
		return "", fmt.Errorf("recover plays signer: %w", err)
	}
	return strings.ToLower(crypto.PubkeyToAddress(*pub).Hex()), nil
}

// Play returns the play processor.
func Play() Processor { return &playProcessor{} }
