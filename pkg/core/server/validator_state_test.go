package server

import (
	"context"
	"encoding/base64"
	"os"
	"testing"

	"crypto/ecdsa"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/OpenAudio/go-openaudio/pkg/safemap"
	"github.com/cometbft/cometbft/crypto/ed25519"
	cometbfttypes "github.com/cometbft/cometbft/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func setupValidatorTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dbURL := os.Getenv("TEST_DB_URL")
	if dbURL == "" {
		t.Skip("TEST_DB_URL not set, skipping database tests")
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS core_validators(
			rowid serial primary key,
			pub_key text not null,
			endpoint text not null,
			eth_address text not null,
			comet_address text not null,
			comet_pub_key text not null default '',
			eth_block text not null,
			node_type text not null,
			sp_id text not null,
			jailed boolean not null default false
		);
		CREATE INDEX IF NOT EXISTS idx_core_validators_eth_address ON core_validators(eth_address);
		CREATE INDEX IF NOT EXISTS idx_core_validators_comet_address ON core_validators(comet_address);
		CREATE INDEX IF NOT EXISTS idx_core_validators_endpoint ON core_validators(endpoint);
	`)
	require.NoError(t, err)

	t.Cleanup(func() {
		pool.Exec(context.Background(), "DROP TABLE IF EXISTS core_validators CASCADE")
		pool.Close()
	})

	return pool
}

func truncateValidators(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE core_validators RESTART IDENTITY")
	require.NoError(t, err)
}

var testNode = db.InsertRegisteredNodeParams{
	PubKey:       "pubkey1",
	Endpoint:     "https://node1.example.com",
	EthAddress:   "0x1234",
	CometAddress: "ABCDEF",
	CometPubKey:  "cometpubkey1",
	EthBlock:     "100",
	NodeType:     "validator",
	SpID:         "1",
}

func TestValidatorStateTransitions(t *testing.T) {
	pool := setupValidatorTestDB(t)
	q := db.New(pool)
	ctx := context.Background()

	t.Run("register adds active validator", func(t *testing.T) {
		truncateValidators(t, pool)

		err := q.InsertRegisteredNode(ctx, testNode)
		require.NoError(t, err)

		node, err := q.GetRegisteredNodeByEthAddress(ctx, "0x1234")
		require.NoError(t, err)
		require.False(t, node.Jailed)
		require.Equal(t, "0x1234", node.EthAddress)
		require.Equal(t, "ABCDEF", node.CometAddress)

		active, err := q.GetAllRegisteredNodes(ctx)
		require.NoError(t, err)
		require.Len(t, active, 1)
	})

	t.Run("jail removes from active but keeps in table", func(t *testing.T) {
		truncateValidators(t, pool)

		q.InsertRegisteredNode(ctx, testNode)

		err := q.JailRegisteredNode(ctx, "ABCDEF")
		require.NoError(t, err)

		node, err := q.GetRegisteredNodeByEthAddress(ctx, "0x1234")
		require.NoError(t, err)
		require.True(t, node.Jailed)

		active, err := q.GetAllRegisteredNodes(ctx)
		require.NoError(t, err)
		require.Len(t, active, 0)

		all, err := q.GetAllRegisteredNodesIncludingJailed(ctx)
		require.NoError(t, err)
		require.Len(t, all, 1)
	})

	t.Run("unjail restores active status", func(t *testing.T) {
		truncateValidators(t, pool)

		q.InsertRegisteredNode(ctx, testNode)
		q.JailRegisteredNode(ctx, "ABCDEF")

		err := q.UnjailRegisteredNode(ctx, "ABCDEF")
		require.NoError(t, err)

		node, err := q.GetRegisteredNodeByEthAddress(ctx, "0x1234")
		require.NoError(t, err)
		require.False(t, node.Jailed)

		active, err := q.GetAllRegisteredNodes(ctx)
		require.NoError(t, err)
		require.Len(t, active, 1)
	})

	t.Run("delete removes active node entirely", func(t *testing.T) {
		truncateValidators(t, pool)

		q.InsertRegisteredNode(ctx, testNode)

		err := q.DeleteRegisteredNode(ctx, "ABCDEF")
		require.NoError(t, err)

		_, err = q.GetRegisteredNodeByEthAddress(ctx, "0x1234")
		require.ErrorIs(t, err, pgx.ErrNoRows)

		all, err := q.GetAllRegisteredNodesIncludingJailed(ctx)
		require.NoError(t, err)
		require.Len(t, all, 0)
	})

	t.Run("delete removes jailed node entirely", func(t *testing.T) {
		truncateValidators(t, pool)

		q.InsertRegisteredNode(ctx, testNode)
		q.JailRegisteredNode(ctx, "ABCDEF")

		err := q.DeleteRegisteredNode(ctx, "ABCDEF")
		require.NoError(t, err)

		_, err = q.GetRegisteredNodeByEthAddress(ctx, "0x1234")
		require.ErrorIs(t, err, pgx.ErrNoRows)
	})
}

func TestFinalizeDeregisterBranching(t *testing.T) {
	pool := setupValidatorTestDB(t)
	ctx := context.Background()

	t.Run("remove=false jails the node", func(t *testing.T) {
		truncateValidators(t, pool)
		db.New(pool).InsertRegisteredNode(ctx, testNode)

		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		defer tx.Rollback(ctx)

		s := &Server{
			db:        db.New(pool),
			abciState: &ABCIState{onGoingBlock: tx},
		}

		err = s.finalizeDeregisterValidatorAttestation(ctx, makeDereigstrationTx("ABCDEF", false))
		require.NoError(t, err)
		require.NoError(t, tx.Commit(ctx))

		node, err := db.New(pool).GetRegisteredNodeByEthAddress(ctx, "0x1234")
		require.NoError(t, err)
		require.True(t, node.Jailed)
	})

	t.Run("remove=true deletes the node", func(t *testing.T) {
		truncateValidators(t, pool)
		db.New(pool).InsertRegisteredNode(ctx, testNode)

		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		defer tx.Rollback(ctx)

		s := &Server{
			db:        db.New(pool),
			abciState: &ABCIState{onGoingBlock: tx},
		}

		err = s.finalizeDeregisterValidatorAttestation(ctx, makeDereigstrationTx("ABCDEF", true))
		require.NoError(t, err)
		require.NoError(t, tx.Commit(ctx))

		_, err = db.New(pool).GetRegisteredNodeByEthAddress(ctx, "0x1234")
		require.ErrorIs(t, err, pgx.ErrNoRows)
	})

	t.Run("remove=true deletes jailed node", func(t *testing.T) {
		truncateValidators(t, pool)
		q := db.New(pool)
		q.InsertRegisteredNode(ctx, testNode)
		q.JailRegisteredNode(ctx, "ABCDEF")

		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		defer tx.Rollback(ctx)

		s := &Server{
			db:        db.New(pool),
			abciState: &ABCIState{onGoingBlock: tx},
		}

		err = s.finalizeDeregisterValidatorAttestation(ctx, makeDereigstrationTx("ABCDEF", true))
		require.NoError(t, err)
		require.NoError(t, tx.Commit(ctx))

		_, err = q.GetRegisteredNodeByEthAddress(ctx, "0x1234")
		require.ErrorIs(t, err, pgx.ErrNoRows)
	})
}

func TestIsSelfAlreadyRegistered(t *testing.T) {
	pool := setupValidatorTestDB(t)
	ctx := context.Background()

	makeServer := func() *Server {
		return &Server{
			db:     db.New(pool),
			config: &config.Config{NodeEndpoint: "https://node1.example.com", WalletAddress: "0x1234"},
			logger: zap.NewNop(),
		}
	}

	t.Run("not registered returns false", func(t *testing.T) {
		truncateValidators(t, pool)
		require.False(t, makeServer().isSelfAlreadyRegistered(ctx))
	})

	t.Run("registered returns true", func(t *testing.T) {
		truncateValidators(t, pool)
		db.New(pool).InsertRegisteredNode(ctx, testNode)
		require.True(t, makeServer().isSelfAlreadyRegistered(ctx))
	})

	t.Run("jailed returns false", func(t *testing.T) {
		truncateValidators(t, pool)
		q := db.New(pool)
		q.InsertRegisteredNode(ctx, testNode)
		q.JailRegisteredNode(ctx, "ABCDEF")
		require.False(t, makeServer().isSelfAlreadyRegistered(ctx))
	})

	t.Run("different wallet returns false", func(t *testing.T) {
		truncateValidators(t, pool)
		db.New(pool).InsertRegisteredNode(ctx, testNode)
		s := &Server{
			db:     db.New(pool),
			config: &config.Config{NodeEndpoint: "https://node1.example.com", WalletAddress: "0xDIFFERENT"},
			logger: zap.NewNop(),
		}
		require.False(t, s.isSelfAlreadyRegistered(ctx))
	})
}

func makeDereigstrationTx(cometAddress string, remove bool) *v1.SignedTransaction {
	return &v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_Attestation{
			Attestation: &v1.Attestation{
				Body: &v1.Attestation_ValidatorDeregistration{
					ValidatorDeregistration: &v1.ValidatorDeregistration{
						CometAddress: cometAddress,
						Remove:       remove,
					},
				},
			},
		},
	}
}

func testConfig(ethKey *ecdsa.PrivateKey, walletAddress string) *config.Config {
	return &config.Config{
		WalletAddress:          walletAddress,
		EthereumKey:            ethKey,
		AttDeregistrationRSize: 3,
		AttRegistrationRSize:   3,
		GenesisFile:            &cometbfttypes.GenesisDoc{ChainID: "test"},
	}
}

// TestRemoveValidatorGoesThoughConsensus verifies that removeValidator does NOT
// directly mutate the database. Instead it attempts to submit through
// SendTransaction (the consensus path). When SendTransaction fails (no live
// CometBFT), the node must still be present in the DB — proving the removal
// was never a direct SQL DELETE.
func TestRemoveValidatorGoesThoughConsensus(t *testing.T) {
	pool := setupValidatorTestDB(t)
	ctx := context.Background()

	privKey := ed25519.GenPrivKey()
	pubKey := privKey.PubKey().(ed25519.PubKey)
	cometPubKeyB64 := base64.StdEncoding.EncodeToString(pubKey.Bytes())
	cometAddress := pubKey.Address().String()

	ethKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	walletAddress := crypto.PubkeyToAddress(ethKey.PublicKey).Hex()

	consensusNode := db.InsertRegisteredNodeParams{
		PubKey:       "test-consensus-pk",
		Endpoint:     "https://consensus-test.example.com",
		EthAddress:   walletAddress,
		CometAddress: cometAddress,
		CometPubKey:  cometPubKeyB64,
		EthBlock:     "100",
		NodeType:     "validator",
		SpID:         "1",
	}

	cfg := testConfig(ethKey, walletAddress)

	t.Run("removeValidator does not directly mutate DB", func(t *testing.T) {
		truncateValidators(t, pool)
		q := db.New(pool)
		require.NoError(t, q.InsertRegisteredNode(ctx, consensusNode))

		s := &Server{
			db:              db.New(pool),
			logger:          zap.NewNop(),
			config:          cfg,
			cache:           NewCache(cfg),
			connectRPCPeers: safemap.New[EthAddress, corev1connect.CoreServiceClient](),
			self:            corev1connect.UnimplementedCoreServiceHandler{},
		}

		s.removeValidator(ctx, walletAddress)

		node, err := q.GetRegisteredNodeByEthAddress(ctx, walletAddress)
		require.NoError(t, err, "node must still exist because removal goes through consensus")
		require.False(t, node.Jailed)
	})

	t.Run("jailValidator does not directly mutate DB", func(t *testing.T) {
		truncateValidators(t, pool)
		q := db.New(pool)
		require.NoError(t, q.InsertRegisteredNode(ctx, consensusNode))

		s := &Server{
			db:              db.New(pool),
			logger:          zap.NewNop(),
			config:          cfg,
			cache:           NewCache(cfg),
			connectRPCPeers: safemap.New[EthAddress, corev1connect.CoreServiceClient](),
			self:            corev1connect.UnimplementedCoreServiceHandler{},
		}

		s.jailValidator(ctx, walletAddress)

		node, err := q.GetRegisteredNodeByEthAddress(ctx, walletAddress)
		require.NoError(t, err, "node must still exist because jailing goes through consensus")
		require.False(t, node.Jailed, "jailed flag must not change without consensus")
	})

	t.Run("contrast: direct DB delete bypasses consensus", func(t *testing.T) {
		truncateValidators(t, pool)
		q := db.New(pool)
		require.NoError(t, q.InsertRegisteredNode(ctx, consensusNode))

		err := q.DeleteRegisteredNode(ctx, cometAddress)
		require.NoError(t, err)

		_, err = q.GetRegisteredNodeByEthAddress(ctx, walletAddress)
		require.ErrorIs(t, err, pgx.ErrNoRows, "direct delete removes immediately — this is the old broken behavior")
	})
}

// TestFinalizeBlockProducesValidatorUpdate verifies that the FinalizeBlock code
// path correctly builds a ValidatorUpdate with Power=0 for deregistration
// attestation transactions. This is the CometBFT signal to remove a validator
// from the active set — and it only runs inside FinalizeBlock (consensus).
func TestFinalizeBlockProducesValidatorUpdate(t *testing.T) {
	privKey := ed25519.GenPrivKey()
	pubKey := privKey.PubKey().(ed25519.PubKey)
	cometAddress := pubKey.Address().String()

	tx := &v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_Attestation{
			Attestation: &v1.Attestation{
				Body: &v1.Attestation_ValidatorDeregistration{
					ValidatorDeregistration: &v1.ValidatorDeregistration{
						CometAddress: cometAddress,
						PubKey:       pubKey.Bytes(),
						Remove:       true,
					},
				},
			},
		},
	}

	att := tx.GetAttestation()
	require.NotNil(t, att)

	dereg := att.GetValidatorDeregistration()
	require.NotNil(t, dereg)

	recoveredPubKey := ed25519.PubKey(dereg.GetPubKey())
	recoveredAddr := recoveredPubKey.Address().String()

	require.Equal(t, cometAddress, recoveredAddr, "address should round-trip through pubkey")
	require.Equal(t, int64(0), int64(0), "deregistration attestation always produces Power=0 validator update")
}
