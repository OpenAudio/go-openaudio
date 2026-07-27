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

func TestProcessProposalRejectsDuplicateERNPartyReferences(t *testing.T) {
	f := newDDEXConsensusFixture(t)
	txBytes := f.makeERNTx(t, []string{"party-1", "party-1"})

	resp, err := f.server.ProcessProposal(f.ctx, &abcitypes.ProcessProposalRequest{
		Height: f.blockHeight,
		Time:   time.Unix(1, 0).UTC(),
		Txs:    [][]byte{txBytes},
	})
	require.NoError(t, err)
	require.Equal(t, abcitypes.PROCESS_PROPOSAL_STATUS_REJECT, resp.Status)
}

func TestFinalizeBlockRejectsDuplicateERNPartyReferencesWithoutPoisoningCommit(t *testing.T) {
	f := newDDEXConsensusFixture(t)
	txBytes := f.makeERNTx(t, []string{"party-1", "party-1"})

	resp, err := f.server.FinalizeBlock(f.ctx, &abcitypes.FinalizeBlockRequest{
		Height: f.blockHeight,
		Hash:   []byte{0x01},
		Time:   time.Unix(1, 0).UTC(),
		Txs:    [][]byte{txBytes},
	})
	require.NoError(t, err)
	require.Len(t, resp.TxResults, 1)
	require.Equal(t, uint32(2), resp.TxResults[0].Code)
	require.NoError(t, f.server.commitInProgressTx(f.ctx))
}

type ddexConsensusFixture struct {
	ctx         context.Context
	server      *Server
	blockHeight int64
}

func newDDEXConsensusFixture(t *testing.T) *ddexConsensusFixture {
	t.Helper()

	f := newRegistrationConsensusFixture(t)
	f.server.config.ProgrammableDistributionEnabled = true
	setupDDEXConsensusTestDB(t, f.server.pool)

	return &ddexConsensusFixture{
		ctx:         f.ctx,
		server:      f.server,
		blockHeight: f.blockHeight,
	}
}

func (f *ddexConsensusFixture) makeERNTx(t *testing.T, partyRefs []string) []byte {
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

func setupDDEXConsensusTestDB(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			DROP TABLE IF EXISTS core_deals;
			DROP TABLE IF EXISTS core_parties;
			DROP TABLE IF EXISTS core_releases;
			DROP TABLE IF EXISTS core_resources;
			DROP TABLE IF EXISTS core_ern;
		`)
	})

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
		CREATE TABLE IF NOT EXISTS core_resources (
			address text primary key,
			ern_address text not null,
			entity_type text not null,
			entity_index integer not null,
			tx_hash text not null,
			block_height bigint not null,
			created_at timestamp default now()
		);
		CREATE TABLE IF NOT EXISTS core_releases (
			address text primary key,
			ern_address text not null,
			entity_type text not null,
			entity_index integer not null,
			tx_hash text not null,
			block_height bigint not null,
			created_at timestamp default now()
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
		CREATE TABLE IF NOT EXISTS core_deals (
			address text primary key,
			ern_address text not null,
			entity_type text not null,
			entity_index integer not null,
			tx_hash text not null,
			block_height bigint not null,
			created_at timestamp default now()
		);
	`)
	require.NoError(t, err)
}
