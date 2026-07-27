package server

import (
	"context"
	"testing"
	"time"

	corev1beta1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1beta1"
	ddexv1beta1 "github.com/OpenAudio/go-openaudio/pkg/api/ddex/v1beta1"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestFinalizeBlockDuplicateV2ERNTransactionDoesNotPoisonCommit(t *testing.T) {
	f := newRegistrationConsensusFixture(t)
	f.server.config.ProgrammableDistributionEnabled = true
	setupV2ERNConsensusTestDB(t, f.server.pool)

	txBytes := makeV2ERNTransaction(t, f, []string{"party-1"})
	height := f.blockHeight + 1
	blockTime := time.Unix(height, 0).UTC()
	processResp, err := f.server.ProcessProposal(f.ctx, &abcitypes.ProcessProposalRequest{
		Height: height,
		Time:   blockTime,
		Txs:    [][]byte{txBytes, txBytes},
	})
	require.NoError(t, err)
	require.Equal(t, abcitypes.PROCESS_PROPOSAL_STATUS_ACCEPT, processResp.Status)

	finalizeResp, err := f.server.FinalizeBlock(f.ctx, &abcitypes.FinalizeBlockRequest{
		Height: height,
		Hash:   []byte{1, 2, 3, 4},
		Time:   blockTime,
		Txs:    [][]byte{txBytes, txBytes},
	})
	require.NoError(t, err)
	require.Len(t, finalizeResp.TxResults, 2)
	require.Equal(t, uint32(0), finalizeResp.TxResults[0].Code)
	require.Equal(t, uint32(2), finalizeResp.TxResults[1].Code)

	commitErr := f.server.commitInProgressTx(f.ctx)
	if commitErr != nil && f.server.abciState.onGoingBlock != nil {
		_ = f.server.abciState.onGoingBlock.Rollback(f.ctx)
		f.server.abciState.onGoingBlock = nil
	}
	require.NoError(t, commitErr)
}

func makeV2ERNTransaction(t *testing.T, f *registrationConsensusFixture, partyRefs []string) []byte {
	t.Helper()

	partyList := make([]*ddexv1beta1.Party, 0, len(partyRefs))
	for _, ref := range partyRefs {
		partyList = append(partyList, &ddexv1beta1.Party{PartyReference: ref})
	}

	controlType := ddexv1beta1.MessageControlType_MESSAGE_CONTROL_TYPE_NEW_MESSAGE
	tx := &corev1beta1.Transaction{
		Signature: &corev1beta1.Signature{Signature: []byte{0xff}},
		Envelope: &corev1beta1.Envelope{
			Header: &corev1beta1.EnvelopeHeader{
				ChainId:    f.server.config.GenesisFile.ChainID,
				Expiration: f.blockHeight + 10,
				From:       "0x0000000000000000000000000000000000000001",
				To:         "0x0000000000000000000000000000000000000002",
			},
			Messages: []*corev1beta1.Message{
				{
					Message: &corev1beta1.Message_Ern{
						Ern: &ddexv1beta1.NewReleaseMessage{
							MessageHeader: &ddexv1beta1.MessageHeader{
								MessageId:          "message-1",
								MessageControlType: &controlType,
							},
							PartyList: partyList,
						},
					},
				},
			},
		},
	}

	txBytes, err := proto.Marshal(tx)
	require.NoError(t, err)
	return txBytes
}

func setupV2ERNConsensusTestDB(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	_, err := pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS core_ern (
			id bigserial primary key,
			address text not null,
			index bigint not null,
			tx_hash text not null,
			sender text not null,
			message_control_type smallint not null,
			raw_message bytea not null,
			raw_acknowledgment bytea not null,
			block_height bigint not null
		);
		CREATE TABLE IF NOT EXISTS core_parties (
			address text primary key,
			ern_address text not null,
			entity_type text not null,
			entity_index integer not null,
			tx_hash text not null,
			block_height bigint not null,
			created_at timestamp default now()
		);
		TRUNCATE core_ern, core_parties;
	`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			DROP TABLE IF EXISTS core_parties;
			DROP TABLE IF EXISTS core_ern;
		`)
	})
}
