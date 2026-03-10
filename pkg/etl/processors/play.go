package processors

import (
	"context"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/etl/db"
	"github.com/jackc/pgx/v5/pgtype"
)

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

// Play returns the play processor.
func Play() Processor { return &playProcessor{} }
