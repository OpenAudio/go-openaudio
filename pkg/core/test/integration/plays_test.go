package integration_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/core/test/integration/utils"
	"github.com/google/uuid"
	protob "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSubmitAndReadPlayThroughGRPC(t *testing.T) {
	ctx := context.Background()
	sdk := utils.DiscoveryOne

	listens := []*corev1.TrackPlay{
		{
			UserId:    uuid.NewString(),
			TrackId:   uuid.NewString(),
			Timestamp: timestamppb.New(time.Now()),
			Signature: "todo: impl",
			City:      uuid.NewString(),
			Region:    uuid.NewString(),
			Country:   uuid.NewString(),
		},
		{
			UserId:    uuid.NewString(),
			TrackId:   uuid.NewString(),
			Timestamp: timestamppb.New(time.Now()),
			Signature: "todo: impl",
			City:      uuid.NewString(),
			Region:    uuid.NewString(),
			Country:   uuid.NewString(),
		},
		{
			UserId:    uuid.NewString(),
			TrackId:   uuid.NewString(),
			Timestamp: timestamppb.New(time.Now()),
			Signature: "todo: impl",
			City:      uuid.NewString(),
			Region:    uuid.NewString(),
			Country:   uuid.NewString(),
		},
	}

	playEvent := &corev1.SignedTransaction{
		Transaction: &corev1.SignedTransaction_Plays{
			Plays: &corev1.TrackPlays{
				Plays: listens,
			},
		},
	}

	expectedTxHash, err := common.ToTxHash(playEvent)
	if err != nil {
		t.Fatalf("Failed to get transaction hash: %v", err)
	}

	req := &corev1.SendTransactionRequest{
		Transaction: playEvent,
	}

	submitRes, err := sdk.Core.SendTransaction(ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatalf("Failed to send transaction: %v", err)
	}

	txhash := submitRes.Msg.Transaction.Hash
	if expectedTxHash != txhash {
		t.Errorf("Expected transaction hash %s, got %s", expectedTxHash, txhash)
	}

	time.Sleep(time.Second * 1)

	playEventRes, err := sdk.Core.GetTransaction(ctx, connect.NewRequest(&corev1.GetTransactionRequest{TxHash: txhash}))
	if err != nil {
		t.Fatalf("Failed to get transaction: %v", err)
	}

	if !protob.Equal(playEvent, playEventRes.Msg.Transaction.Transaction) {
		t.Error("Retrieved transaction does not match submitted transaction")
	}
}
