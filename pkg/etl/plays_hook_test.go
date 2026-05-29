package etl

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestRegisterPlaysHook_FiresInOrder verifies that hooks registered via
// RegisterPlaysHook run in registration order and receive the decoded plays
// plus block context.
func TestRegisterPlaysHook_FiresInOrder(t *testing.T) {
	e := &Indexer{logger: zap.NewNop()}

	var order []int
	var gotPlays []*corev1.TrackPlay
	var gotHeight int64
	var gotHash string

	e.RegisterPlaysHook(func(_ context.Context, p *PlaysParams) error {
		order = append(order, 1)
		gotPlays = p.Plays
		gotHeight = p.BlockHeight
		gotHash = p.TxHash
		return nil
	})
	e.RegisterPlaysHook(func(_ context.Context, _ *PlaysParams) error {
		order = append(order, 2)
		return nil
	})

	plays := []*corev1.TrackPlay{{UserId: "1", TrackId: "2"}}
	block := &corev1.Block{
		Height:    42,
		Hash:      "blockhash",
		Timestamp: timestamppb.New(time.Unix(1000, 0)),
	}

	e.firePlaysHooks(context.Background(), nil, plays, block, "txhash")

	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("expected hooks to fire in order [1 2], got %v", order)
	}
	if len(gotPlays) != 1 || gotPlays[0].UserId != "1" {
		t.Errorf("hook did not receive plays slice: %v", gotPlays)
	}
	if gotHeight != 42 {
		t.Errorf("expected block height 42, got %d", gotHeight)
	}
	if gotHash != "txhash" {
		t.Errorf("expected tx hash 'txhash', got %q", gotHash)
	}
}

// TestRegisterPlaysHook_ErrorIsNonFatal verifies a hook returning an error
// does not stop subsequent hooks from running — mirroring the em.PostHook
// log-and-continue contract.
func TestRegisterPlaysHook_ErrorIsNonFatal(t *testing.T) {
	e := &Indexer{logger: zap.NewNop()}

	secondFired := false
	e.RegisterPlaysHook(func(_ context.Context, _ *PlaysParams) error {
		return errors.New("boom")
	})
	e.RegisterPlaysHook(func(_ context.Context, _ *PlaysParams) error {
		secondFired = true
		return nil
	})

	block := &corev1.Block{Height: 1, Timestamp: timestamppb.New(time.Unix(0, 0))}
	e.firePlaysHooks(context.Background(), nil, nil, block, "tx")

	if !secondFired {
		t.Error("second hook did not fire after first returned an error")
	}
}

// TestFirePlaysHooks_NoHooks is a no-op fast path that must not panic when
// no hooks are registered.
func TestFirePlaysHooks_NoHooks(t *testing.T) {
	e := &Indexer{logger: zap.NewNop()}
	block := &corev1.Block{Height: 1, Timestamp: timestamppb.New(time.Unix(0, 0))}
	e.firePlaysHooks(context.Background(), nil, nil, block, "tx")
}
