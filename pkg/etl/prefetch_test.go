package etl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"go.uber.org/zap"
)

// indexOrderTxs is a block's transactions in the order the chain executed
// them: an Update before the Create of a different entity, and plays with
// differing signatures. The prefetcher must hand them on unchanged.
func indexOrderTxs(height int64) []*corev1.Transaction {
	me := func(action string, entityID int64, sig string) *corev1.Transaction {
		return &corev1.Transaction{Height: height, Transaction: &corev1.SignedTransaction{
			Signature: sig,
			Transaction: &corev1.SignedTransaction_ManageEntity{ManageEntity: &corev1.ManageEntityLegacy{
				EntityType: "User", EntityId: entityID, Action: action,
			}},
		}}
	}
	play := func(sig string) *corev1.Transaction {
		return &corev1.Transaction{Height: height, Transaction: &corev1.SignedTransaction{
			Signature:   sig,
			Transaction: &corev1.SignedTransaction_Plays{Plays: &corev1.TrackPlays{}},
		}}
	}
	return []*corev1.Transaction{
		me("Update", 2, "update-user-2"),
		play("play-b"),
		play("play-a"),
		me("Create", 3, "create-user-3"),
	}
}

func signatures(txs []*corev1.Transaction) []string {
	sigs := make([]string, len(txs))
	for i, tx := range txs {
		sigs[i] = tx.GetTransaction().GetSignature()
	}
	return sigs
}

// startBlocksServer serves GetBlocks and GetBlock for heights below head,
// recording whether every request asked for canonical order.
func startBlocksServer(t *testing.T, head int64, allCanon *atomic.Bool) corev1connect.CoreServiceClient {
	t.Helper()
	allCanon.Store(true)
	mux := http.NewServeMux()
	mux.Handle(corev1connect.CoreServiceGetBlocksProcedure, connect.NewUnaryHandler(
		corev1connect.CoreServiceGetBlocksProcedure,
		func(ctx context.Context, req *connect.Request[corev1.GetBlocksRequest]) (*connect.Response[corev1.GetBlocksResponse], error) {
			if !req.Msg.Canon {
				allCanon.Store(false)
			}
			blocks := map[int64]*corev1.Block{}
			for _, h := range req.Msg.Height {
				if h < head {
					blocks[h] = &corev1.Block{Height: h, Transactions: indexOrderTxs(h)}
				}
			}
			return connect.NewResponse(&corev1.GetBlocksResponse{Blocks: blocks, CurrentHeight: head - 1}), nil
		},
	))
	mux.Handle(corev1connect.CoreServiceGetBlockProcedure, connect.NewUnaryHandler(
		corev1connect.CoreServiceGetBlockProcedure,
		func(ctx context.Context, req *connect.Request[corev1.GetBlockRequest]) (*connect.Response[corev1.GetBlockResponse], error) {
			if !req.Msg.Canon {
				allCanon.Store(false)
			}
			h := req.Msg.Height
			if h >= head {
				return connect.NewResponse(&corev1.GetBlockResponse{Block: &corev1.Block{Height: -1}}), nil
			}
			return connect.NewResponse(&corev1.GetBlockResponse{
				Block:         &corev1.Block{Height: h, Transactions: indexOrderTxs(h)},
				CurrentHeight: head - 1,
			}), nil
		},
	))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return corev1connect.NewCoreServiceClient(srv.Client(), srv.URL)
}

// TestPrefetcherRequestsCanonicalOrder verifies the polling path asks core for
// transactions in chain order and delivers them in that order, for both the
// batched GetBlocks fetch and the single-block GetBlock fallback.
func TestPrefetcherRequestsCanonicalOrder(t *testing.T) {
	for _, batch := range []bool{true, false} {
		name := "GetBlock"
		if batch {
			name = "GetBlocks"
		}
		t.Run(name, func(t *testing.T) {
			var allCanon atomic.Bool
			client := startBlocksServer(t, 4, &allCanon)
			p := newPrefetcher(client, zap.NewNop())
			p.batchEnabled = batch

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go p.run(ctx, 1)

			want := signatures(indexOrderTxs(0))
			timeout := time.After(5 * time.Second)
			for h := int64(1); h < 4; h++ {
				select {
				case pb := <-p.C():
					if pb.Block.Height != h {
						t.Fatalf("got height %d, want %d", pb.Block.Height, h)
					}
					if got := signatures(pb.Block.Transactions); !slices.Equal(got, want) {
						t.Fatalf("block %d order = %v, want %v", h, got, want)
					}
				case <-timeout:
					t.Fatalf("timed out waiting for block %d", h)
				}
			}
			cancel()
			if !allCanon.Load() {
				t.Fatal("prefetcher requested blocks without canonical order")
			}
		})
	}
}
