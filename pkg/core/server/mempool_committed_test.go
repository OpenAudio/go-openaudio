package server

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type committedTxFixture struct {
	ctx    context.Context
	q      *db.Queries
	server *Server
}

func newCommittedTxFixture(t *testing.T) *committedTxFixture {
	t.Helper()

	dbURL := os.Getenv("TEST_DB_URL")
	if dbURL == "" {
		t.Skip("TEST_DB_URL not set, skipping database tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		DROP TABLE IF EXISTS core_transactions;
		CREATE TABLE core_transactions(
			rowid bigserial primary key,
			block_id bigint not null,
			index integer not null,
			tx_hash text not null,
			transaction bytea not null,
			created_at timestamp not null,
			unique (block_id, index)
		);
		CREATE INDEX idx_core_transactions_tx_hash_lower ON core_transactions (LOWER(tx_hash));
	`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS core_transactions")
		pool.Close()
	})

	q := db.New(pool)
	// a rollup interval that is never reached keeps PrepareProposal from
	// building an SLA rollup
	cfg := &config.Config{SlaRollupInterval: 1 << 30}
	logger := zap.NewNop()
	return &committedTxFixture{
		ctx: ctx,
		q:   q,
		server: &Server{
			db:     q,
			logger: logger,
			config: cfg,
			mempl:  NewMempool(logger, cfg, q, 100),
		},
	}
}

func committedPlayTx(t *testing.T, trackID string) (*MempoolTransaction, []byte, string) {
	t.Helper()
	tx := &v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_Plays{
			Plays: &v1.TrackPlays{Plays: []*v1.TrackPlay{{
				UserId:    "1",
				TrackId:   trackID,
				Timestamp: timestamppb.New(time.Unix(1700000000, 0)),
			}}},
		},
	}
	txBytes, err := proto.Marshal(tx)
	require.NoError(t, err)
	return &MempoolTransaction{Tx: tx, Deadline: 100}, txBytes, common.ToTxHashFromBytes(txBytes)
}

// commit stores the tx the way FinalizeBlock does.
func (f *committedTxFixture) commit(t *testing.T, height int64, txBytes []byte, txHash string) {
	t.Helper()
	require.NoError(t, f.q.StoreTransaction(f.ctx, db.StoreTransactionParams{
		BlockID:     height,
		Index:       0,
		TxHash:      txHash,
		Transaction: txBytes,
		CreatedAt:   f.q.ToPgxTimestamp(time.Now()),
	}))
}

func TestAddMempoolTransactionRefusesCommittedTx(t *testing.T) {
	f := newCommittedTxFixture(t)

	committedTx, committedBytes, committedHash := committedPlayTx(t, "1")
	f.commit(t, 1, committedBytes, committedHash)
	require.ErrorIs(t, f.server.addMempoolTransaction(f.ctx, committedHash, committedTx, false), ErrTxAlreadyCommitted)

	freshTx, _, freshHash := committedPlayTx(t, "2")
	require.NoError(t, f.server.addMempoolTransaction(f.ctx, freshHash, freshTx, false))

	count, _ := f.server.mempl.MempoolSize()
	require.Equal(t, 1, count)
}

// Nodes gossip admitted txs asynchronously, so a peer's forward can arrive
// after the block carrying the tx has committed. It must not re-enter the
// mempool, or this node proposes the tx a second time.
func TestForwardAfterCommitIsNotReadmitted(t *testing.T) {
	f := newCommittedTxFixture(t)

	tx, txBytes, txHash := committedPlayTx(t, "1")
	require.NoError(t, f.server.addMempoolTransaction(f.ctx, txHash, tx, false))

	// the block commits: FinalizeBlock stores the tx, Commit clears it
	f.commit(t, 1, txBytes, txHash)
	f.server.mempl.RemoveBatch([]string{txHash})

	// a peer's forward of the same tx arrives late
	require.ErrorIs(t, f.server.addMempoolTransaction(f.ctx, txHash, tx, false), ErrTxAlreadyCommitted)
	count, _ := f.server.mempl.MempoolSize()
	require.Zero(t, count)
}

// A tx can be admitted between a block's db commit and Commit's RemoveBatch.
// PrepareProposal must leave it out and drop it from the mempool.
func TestPrepareProposalDropsCommittedTx(t *testing.T) {
	f := newCommittedTxFixture(t)

	committedTx, committedBytes, committedHash := committedPlayTx(t, "1")
	freshTx, freshBytes, freshHash := committedPlayTx(t, "2")
	require.NoError(t, f.server.addMempoolTransaction(f.ctx, committedHash, committedTx, false))
	require.NoError(t, f.server.addMempoolTransaction(f.ctx, freshHash, freshTx, false))

	// committed after admission, before RemoveBatch ran
	f.commit(t, 1, committedBytes, committedHash)

	resp, err := f.server.PrepareProposal(f.ctx, &abcitypes.PrepareProposalRequest{
		Height:     2,
		Time:       time.Now(),
		MaxTxBytes: 1 << 20,
	})
	require.NoError(t, err)
	require.Equal(t, [][]byte{freshBytes}, resp.Txs)

	count, _ := f.server.mempl.MempoolSize()
	require.Equal(t, 1, count)
}

// Hashes are stored uppercase; the lookup must not depend on the caller's
// case.
func TestCommittedTxHashesIgnoresCase(t *testing.T) {
	f := newCommittedTxFixture(t)

	_, txBytes, txHash := committedPlayTx(t, "1")
	f.commit(t, 1, txBytes, txHash)

	lower := strings.ToLower(txHash)
	require.NotEqual(t, txHash, lower)
	committed, err := f.server.committedTxHashes(f.ctx, []string{lower, "deadbeef"})
	require.NoError(t, err)
	require.Equal(t, map[string]struct{}{lower: {}}, committed)

	exists, err := f.server.isCommittedTx(f.ctx, lower)
	require.NoError(t, err)
	require.True(t, exists)
}
