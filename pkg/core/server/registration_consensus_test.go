package server

import (
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/crypto/ed25519"
	cometbfttypes "github.com/cometbft/cometbft/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

func TestFinalizeBlockSkipsValidatorUpdateForNoopRegistration(t *testing.T) {
	f := newRegistrationConsensusFixture(t)

	firstCometKey := ed25519.GenPrivKey()
	secondCometKey := ed25519.GenPrivKey()
	firstTx := f.makeRegistration(t, firstCometKey)
	secondTx := f.makeRegistration(t, secondCometKey)
	firstCometAddress := firstCometKey.PubKey().Address().String()
	secondCometAddress := secondCometKey.PubKey().Address().String()

	require.NoError(t, f.server.isValidAttestation(f.ctx, firstTx, f.blockHeight))
	require.NoError(t, f.server.isValidAttestation(f.ctx, secondTx, f.blockHeight))

	resp := f.finalizeBlock(t, firstTx, secondTx)
	_, err := f.server.getDb().GetRegisteredNodeByCometAddress(f.ctx, firstCometAddress)
	require.NoError(t, err)
	_, err = f.server.getDb().GetRegisteredNodeByCometAddress(f.ctx, secondCometAddress)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	requireValidatorUpdatesHaveAppRows(t, f, resp.ValidatorUpdates)
}

func TestFinalizeBlockDoesNotUnjailMismatchedCometRegistration(t *testing.T) {
	f := newRegistrationConsensusFixture(t)
	existingCometKey := ed25519.GenPrivKey()
	existingCometPubKey := existingCometKey.PubKey().(ed25519.PubKey)
	require.NoError(t, f.q.InsertRegisteredNode(f.ctx, db.InsertRegisteredNodeParams{
		PubKey:       "existing-pubkey",
		Endpoint:     "https://jailed-validator.example.com",
		EthAddress:   f.delegateWallet,
		CometAddress: existingCometPubKey.Address().String(),
		CometPubKey:  base64.StdEncoding.EncodeToString(existingCometPubKey.Bytes()),
		EthBlock:     "100",
		NodeType:     "validator",
		SpID:         "999",
	}))
	require.NoError(t, f.q.JailRegisteredNode(f.ctx, existingCometPubKey.Address().String()))

	mismatchedCometKey := ed25519.GenPrivKey()
	tx := f.makeRegistration(t, mismatchedCometKey)
	require.NoError(t, f.server.isValidAttestation(f.ctx, tx, f.blockHeight))

	resp := f.finalizeBlock(t, tx)
	node, err := f.server.getDb().GetRegisteredNodeByCometAddress(f.ctx, existingCometPubKey.Address().String())
	require.NoError(t, err)
	require.True(t, node.Jailed)
	require.Empty(t, resp.ValidatorUpdates)
}

func TestFinalizeBlockUnjailsMatchingCometRegistration(t *testing.T) {
	f := newRegistrationConsensusFixture(t)
	existingCometKey := ed25519.GenPrivKey()
	existingCometPubKey := existingCometKey.PubKey().(ed25519.PubKey)
	require.NoError(t, f.q.InsertRegisteredNode(f.ctx, db.InsertRegisteredNodeParams{
		PubKey:       "existing-pubkey",
		Endpoint:     "https://jailed-validator.example.com",
		EthAddress:   f.delegateWallet,
		CometAddress: existingCometPubKey.Address().String(),
		CometPubKey:  base64.StdEncoding.EncodeToString(existingCometPubKey.Bytes()),
		EthBlock:     "100",
		NodeType:     "validator",
		SpID:         "999",
	}))
	require.NoError(t, f.q.JailRegisteredNode(f.ctx, existingCometPubKey.Address().String()))

	tx := f.makeRegistration(t, existingCometKey)
	require.NoError(t, f.server.isValidAttestation(f.ctx, tx, f.blockHeight))

	resp := f.finalizeBlock(t, tx)
	node, err := f.server.getDb().GetRegisteredNodeByCometAddress(f.ctx, existingCometPubKey.Address().String())
	require.NoError(t, err)
	require.False(t, node.Jailed)
	require.Len(t, resp.ValidatorUpdates, 1)
	requireValidatorUpdatesHaveAppRows(t, f, resp.ValidatorUpdates)
}

type registrationConsensusFixture struct {
	ctx             context.Context
	q               *db.Queries
	server          *Server
	attestorSigners []func([]byte) string
	delegateKey     *ecdsa.PrivateKey
	delegateWallet  string
	blockHeight     int64
	nextBlockOffset int64
}

func newRegistrationConsensusFixture(t *testing.T) *registrationConsensusFixture {
	t.Helper()

	pool := setupValidatorTestDB(t)
	ctx := context.Background()
	truncateValidators(t, pool)
	setupFinalizeBlockTestDB(t, pool)

	q := db.New(pool)
	attestorSigners := make([]func([]byte) string, 0, 3)
	for i := 0; i < 3; i++ {
		key, err := crypto.GenerateKey()
		require.NoError(t, err)
		addr := crypto.PubkeyToAddress(key.PublicKey).Hex()
		cometKey := ed25519.GenPrivKey()
		cometPubKey := cometKey.PubKey().(ed25519.PubKey)
		require.NoError(t, q.InsertRegisteredNode(ctx, db.InsertRegisteredNodeParams{
			PubKey:       fmt.Sprintf("attestor-pubkey-%d", i),
			Endpoint:     fmt.Sprintf("https://attestor-%d.example.com", i),
			EthAddress:   addr,
			CometAddress: cometPubKey.Address().String(),
			CometPubKey:  base64.StdEncoding.EncodeToString(cometPubKey.Bytes()),
			EthBlock:     "100",
			NodeType:     "validator",
			SpID:         fmt.Sprintf("%d", i+1),
		}))
		attestorSigners = append(attestorSigners, func(key *ecdsa.PrivateKey) func([]byte) string {
			return func(body []byte) string {
				sig, err := common.EthSign(key, body)
				require.NoError(t, err)
				return sig
			}
		}(key))
	}

	delegateKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	cfg := &config.Config{
		ValidatorVotingPower: 10,
		AttRegistrationRSize: 3,
		AttRegistrationMin:   3,
		GenesisFile:          &cometbfttypes.GenesisDoc{ChainID: "test"},
	}
	server := &Server{
		pool:      pool,
		db:        q,
		logger:    zap.NewNop(),
		config:    cfg,
		cache:     NewCache(cfg),
		abciState: NewABCIState(0),
	}
	t.Cleanup(func() {
		if server.abciState.onGoingBlock != nil {
			_ = server.abciState.onGoingBlock.Rollback(ctx)
			server.abciState.onGoingBlock = nil
		}
	})

	return &registrationConsensusFixture{
		ctx:             ctx,
		q:               q,
		server:          server,
		attestorSigners: attestorSigners,
		delegateKey:     delegateKey,
		delegateWallet:  crypto.PubkeyToAddress(delegateKey.PublicKey).Hex(),
		blockHeight:     1001,
	}
}

func (f *registrationConsensusFixture) makeRegistration(t *testing.T, cometKey ed25519.PrivKey) *v1.SignedTransaction {
	t.Helper()

	reg := &v1.ValidatorRegistration{
		DelegateWallet: f.delegateWallet,
		Endpoint:       "https://new-validator.example.com",
		NodeType:       "validator",
		SpId:           "999",
		EthBlock:       200,
		CometAddress:   cometKey.PubKey().Address().String(),
		PubKey:         cometKey.PubKey().Bytes(),
		Power:          10,
		Deadline:       f.blockHeight + 10,
	}
	bodyBytes, err := proto.Marshal(reg)
	require.NoError(t, err)
	signatures := make([]string, 0, len(f.attestorSigners))
	for _, sign := range f.attestorSigners {
		signatures = append(signatures, sign(bodyBytes))
	}
	att := &v1.Attestation{
		Signatures: signatures,
		Body:       &v1.Attestation_ValidatorRegistration{ValidatorRegistration: reg},
	}
	attBytes, err := proto.Marshal(att)
	require.NoError(t, err)
	txSig, err := common.EthSign(f.delegateKey, attBytes)
	require.NoError(t, err)
	return &v1.SignedTransaction{
		Signature: txSig,
		Transaction: &v1.SignedTransaction_Attestation{
			Attestation: att,
		},
	}
}

func (f *registrationConsensusFixture) finalizeBlock(t *testing.T, txs ...*v1.SignedTransaction) *abcitypes.FinalizeBlockResponse {
	t.Helper()

	txBytes := make([][]byte, 0, len(txs))
	for _, tx := range txs {
		bytes, err := proto.Marshal(tx)
		require.NoError(t, err)
		txBytes = append(txBytes, bytes)
	}
	height := f.blockHeight + f.nextBlockOffset
	f.nextBlockOffset++
	resp, err := f.server.FinalizeBlock(f.ctx, &abcitypes.FinalizeBlockRequest{
		Height: height,
		Hash:   []byte{0x01},
		Time:   time.Unix(1, 0).UTC(),
		Txs:    txBytes,
	})
	require.NoError(t, err)
	return resp
}

func requireValidatorUpdatesHaveAppRows(t *testing.T, f *registrationConsensusFixture, updates abcitypes.ValidatorUpdates) {
	t.Helper()

	for _, update := range updates {
		cometAddress := ed25519.PubKey(update.PubKeyBytes).Address().String()
		_, err := f.server.getDb().GetRegisteredNodeByCometAddress(f.ctx, cometAddress)
		require.NoErrorf(t, err, "Comet validator update emitted for %s, but app state has no core_validators row", cometAddress)
	}
}

func setupFinalizeBlockTestDB(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			DROP TABLE IF EXISTS validator_history;
			DROP TABLE IF EXISTS sla_node_reports;
			DROP TABLE IF EXISTS sla_rollups;
			DROP TABLE IF EXISTS core_tx_stats;
			DROP TABLE IF EXISTS core_app_state;
			DROP TABLE IF EXISTS core_transactions;
			DROP TABLE IF EXISTS core_blocks;
			DROP TYPE IF EXISTS validator_event;
		`)
	})

	_, err := pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS core_blocks(
			height bigint primary key,
			chain_id text not null,
			hash text not null,
			proposer text not null,
			created_at timestamp not null
		);
		CREATE TABLE IF NOT EXISTS core_transactions(
			block_id bigint not null,
			index int not null,
			tx_hash text not null,
			transaction bytea not null,
			created_at timestamp not null
		);
		CREATE TABLE IF NOT EXISTS core_app_state(
			block_height bigint not null,
			app_hash bytea not null,
			created_at timestamp default current_timestamp,
			primary key (block_height, app_hash)
		);
		CREATE TABLE IF NOT EXISTS core_tx_stats(
			id serial primary key,
			tx_type text not null,
			tx_hash text not null unique,
			block_height bigint not null,
			created_at timestamp default current_timestamp
		);
		CREATE TABLE IF NOT EXISTS sla_rollups(
			id serial primary key,
			tx_hash text not null,
			block_start bigint not null,
			block_end bigint not null,
			time timestamp not null
		);
		CREATE TABLE IF NOT EXISTS sla_node_reports(
			id serial primary key,
			address varchar not null,
			blocks_proposed int not null,
			sla_rollup_id int references sla_rollups,
			unique (address, sla_rollup_id)
		);
		DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'validator_event') THEN
				CREATE TYPE validator_event AS ENUM ('registered', 'deregistered');
			END IF;
		END $$;
		CREATE TABLE IF NOT EXISTS validator_history(
			rowid serial primary key,
			endpoint text not null,
			eth_address text not null,
			comet_address text not null,
			sp_id bigint not null,
			service_type text not null,
			event_type validator_event not null,
			event_time timestamp not null,
			event_block bigint not null
		);
	`)
	require.NoError(t, err)
}
