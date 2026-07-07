package server

import (
	"context"
	"crypto/ecdsa"
	stded25519 "crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mr-tron/base58/base58"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestFinalizeBlockDuplicateRewardPoolCreateCommitsFirstCreate(t *testing.T) {
	fixture := newRegistrationConsensusFixture(t)
	setupRewardPoolConsensusTestDB(t, fixture.server.pool)

	firstAuthorityKey, err := ethcrypto.GenerateKey()
	require.NoError(t, err)
	secondAuthorityKey, err := ethcrypto.GenerateKey()
	require.NoError(t, err)
	rmPub, rmPriv, err := stded25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	deadline := fixture.blockHeight + 100
	tx1 := makeRewardPoolCreateTx(t, firstAuthorityKey, rmPub, rmPriv, deadline)
	tx2 := makeRewardPoolCreateTx(t, secondAuthorityKey, rmPub, rmPriv, deadline+1)
	txBytes := marshalSignedTransactions(t, tx1, tx2)

	height := fixture.blockHeight + 1
	blockTime := time.Unix(height, 0).UTC()
	processResp, err := fixture.server.ProcessProposal(fixture.ctx, &abcitypes.ProcessProposalRequest{
		Height: height,
		Time:   blockTime,
		Txs:    txBytes,
	})
	require.NoError(t, err)
	require.Equal(t, abcitypes.PROCESS_PROPOSAL_STATUS_ACCEPT, processResp.Status)

	finalizeResp, err := fixture.server.FinalizeBlock(fixture.ctx, &abcitypes.FinalizeBlockRequest{
		Height: height,
		Hash:   []byte{1, 2, 3, 4},
		Time:   blockTime,
		Txs:    txBytes,
	})
	require.NoError(t, err)
	require.Len(t, finalizeResp.TxResults, 2)
	require.Equal(t, uint32(0), finalizeResp.TxResults[0].Code)
	require.Equal(t, uint32(0), finalizeResp.TxResults[1].Code)
	require.NoError(t, fixture.server.commitInProgressTx(fixture.ctx))

	pool, err := fixture.server.db.GetRewardPool(fixture.ctx, base58.Encode(rmPub))
	require.NoError(t, err)
	require.Equal(t, []string{strings.ToLower(ethAddress(firstAuthorityKey))}, pool.Authorities)
}

func setupRewardPoolConsensusTestDB(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS core_reward_pools (
			rewards_manager_pubkey text primary key,
			authorities text[] not null default '{}',
			created_at timestamp with time zone default now(),
			updated_at timestamp with time zone default now()
		);
		TRUNCATE core_reward_pools;
	`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS core_reward_pools")
	})
}

func makeRewardPoolCreateTx(t *testing.T, authorityKey *ecdsa.PrivateKey, rmPub stded25519.PublicKey, rmPriv stded25519.PrivateKey, deadline int64) *v1.SignedTransaction {
	t.Helper()

	body := &v1.RewardPoolBody{
		DeadlineBlockHeight: deadline,
		Action: &v1.RewardPoolBody_Create{
			Create: &v1.CreateRewardPool{
				RewardsManagerPubkey: base58.Encode(rmPub),
				Authorities:          []string{ethAddress(authorityKey)},
			},
		},
	}
	bodyBytes, err := common.ProtoSignableBytes(body)
	require.NoError(t, err)
	signature, err := common.ProtoSign(authorityKey, body)
	require.NoError(t, err)
	return &v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_RewardPool{
			RewardPool: &v1.RewardPoolMessage{
				Body:             body,
				Signature:        signature,
				RmOwnerSignature: stded25519.Sign(rmPriv, bodyBytes),
			},
		},
	}
}

func ethAddress(key *ecdsa.PrivateKey) string {
	return ethcrypto.PubkeyToAddress(key.PublicKey).Hex()
}

func marshalSignedTransactions(t *testing.T, txs ...*v1.SignedTransaction) [][]byte {
	t.Helper()

	txBytes := make([][]byte, 0, len(txs))
	for _, tx := range txs {
		b, err := proto.Marshal(tx)
		require.NoError(t, err)
		txBytes = append(txBytes, b)
	}
	return txBytes
}
