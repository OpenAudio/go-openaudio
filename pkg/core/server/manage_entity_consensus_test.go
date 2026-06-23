package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestFinalizeBlockManageEntityMetadataErrorDoesNotPoisonCommit(t *testing.T) {
	f := newManageEntityConsensusFixture(t)
	txBytes := marshalManageEntitySignedTransactions(t,
		makeTrackManageEntityTx(1, "QmSharedCID"),
		makeTrackManageEntityTx(2, "QmSharedCID"),
	)

	height := f.blockHeight + 1
	blockTime := time.Unix(height, 0).UTC()
	processResp, err := f.server.ProcessProposal(f.ctx, &abcitypes.ProcessProposalRequest{
		Height: height,
		Time:   blockTime,
		Txs:    txBytes,
	})
	require.NoError(t, err)
	require.Equal(t, abcitypes.PROCESS_PROPOSAL_STATUS_ACCEPT, processResp.Status)

	finalizeResp, err := f.server.FinalizeBlock(f.ctx, &abcitypes.FinalizeBlockRequest{
		Height: height,
		Hash:   []byte{1, 2, 3, 4},
		Time:   blockTime,
		Txs:    txBytes,
	})
	require.NoError(t, err)
	require.Len(t, finalizeResp.TxResults, 2)
	require.Equal(t, uint32(0), finalizeResp.TxResults[0].Code)
	require.Equal(t, uint32(0), finalizeResp.TxResults[1].Code)
	require.NoError(t, f.server.commitInProgressTx(f.ctx))
}

type manageEntityConsensusFixture struct {
	ctx         context.Context
	server      *Server
	blockHeight int64
}

func newManageEntityConsensusFixture(t *testing.T) *manageEntityConsensusFixture {
	t.Helper()

	f := newRegistrationConsensusFixture(t)
	setupManageEntityConsensusTestDB(t, f.server.pool)
	return &manageEntityConsensusFixture{
		ctx:         f.ctx,
		server:      f.server,
		blockHeight: f.blockHeight,
	}
}

func makeTrackManageEntityTx(trackID int64, cid string) *v1.SignedTransaction {
	metadata := fmt.Sprintf(`{"access_authorities":["0x0000000000000000000000000000000000000001"],"data":{"track_cid":%q}}`, cid)
	return &v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_ManageEntity{
			ManageEntity: &v1.ManageEntityLegacy{
				EntityType: "Track",
				EntityId:   trackID,
				Action:     "Create",
				Metadata:   metadata,
			},
		},
	}
}

func marshalManageEntitySignedTransactions(t *testing.T, txs ...*v1.SignedTransaction) [][]byte {
	t.Helper()

	txBytes := make([][]byte, 0, len(txs))
	for _, tx := range txs {
		b, err := proto.Marshal(tx)
		require.NoError(t, err)
		txBytes = append(txBytes, b)
	}
	return txBytes
}

func setupManageEntityConsensusTestDB(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	_, err := pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS sound_recordings(
			id serial primary key,
			sound_recording_id text not null,
			track_id text not null,
			cid text not null unique,
			encoding_details text
		);
		CREATE TABLE IF NOT EXISTS management_keys(
			id serial primary key,
			track_id text not null,
			pub_key text not null
		);
		TRUNCATE sound_recordings, management_keys;
	`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			DROP TABLE IF EXISTS management_keys;
			DROP TABLE IF EXISTS sound_recordings;
		`)
	})
}
