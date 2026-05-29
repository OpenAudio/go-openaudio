package etl

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakePlaysHookTx struct {
	beginCount    int
	commitCount   int
	rollbackCount int
}

func (f *fakePlaysHookTx) Begin(context.Context) (pgx.Tx, error) {
	f.beginCount++
	return &fakePlaysHookTx{}, nil
}

func (f *fakePlaysHookTx) Commit(context.Context) error {
	f.commitCount++
	return nil
}

func (f *fakePlaysHookTx) Rollback(context.Context) error {
	f.rollbackCount++
	return nil
}

func (*fakePlaysHookTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, nil
}

func (*fakePlaysHookTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	return nil
}

func (*fakePlaysHookTx) LargeObjects() pgx.LargeObjects {
	return pgx.LargeObjects{}
}

func (*fakePlaysHookTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, nil
}

func (*fakePlaysHookTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (*fakePlaysHookTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

func (*fakePlaysHookTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

func (*fakePlaysHookTx) Conn() *pgx.Conn {
	return nil
}

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

	e.firePlaysHooks(context.Background(), &fakePlaysHookTx{}, plays, block, "txhash")

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
	e.firePlaysHooks(context.Background(), &fakePlaysHookTx{}, nil, block, "tx")

	if !secondFired {
		t.Error("second hook did not fire after first returned an error")
	}
}

// TestFirePlaysHooks_NoHooks is a no-op fast path that must not panic when
// no hooks are registered.
func TestFirePlaysHooks_NoHooks(t *testing.T) {
	e := &Indexer{logger: zap.NewNop()}
	block := &corev1.Block{Height: 1, Timestamp: timestamppb.New(time.Unix(0, 0))}
	e.firePlaysHooks(context.Background(), &fakePlaysHookTx{}, nil, block, "tx")
}
