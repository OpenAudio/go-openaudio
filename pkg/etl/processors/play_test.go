package processors

import (
	"context"
	"testing"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/etl/db"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestPlayProcessor_Process verifies the play processor handles valid input.
func TestPlayProcessor_Process(t *testing.T) {
	txCtx := &TxContext{
		Block: &corev1.Block{
			Height:    100,
			Timestamp: timestamppb.Now(),
		},
		TxHash:    "abc123",
		TxIndex:   0,
		BlockTime: pgtype.Timestamp{Valid: true},
		InsertTx:  db.InsertTransactionParams{},
	}

	p := Play()
	if p.TxType() != TxTypePlay {
		t.Errorf("TxType() = %q, want %q", p.TxType(), TxTypePlay)
	}

	// Test empty plays - returns early without calling InsertPlays
	txEmpty := &corev1.SignedTransaction{
		Transaction: &corev1.SignedTransaction_Plays{
			Plays: &corev1.TrackPlays{Plays: []*corev1.TrackPlay{}},
		},
	}
	txCtx2 := &TxContext{
		Block:     txCtx.Block,
		TxHash:    "def456",
		TxIndex:   1,
		BlockTime: txCtx.BlockTime,
		InsertTx:  db.InsertTransactionParams{},
	}
	res, err := p.Process(context.Background(), txEmpty, txCtx2, nil)
	if err != nil {
		t.Fatalf("Process(empty plays) err = %v", err)
	}
	if res.InsertTx.TxType != TxTypePlay {
		t.Errorf("empty plays: InsertTx.TxType = %q, want %q", res.InsertTx.TxType, TxTypePlay)
	}
}
