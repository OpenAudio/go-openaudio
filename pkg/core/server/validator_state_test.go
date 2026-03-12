package server

import (
	"context"
	"os"
	"testing"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
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
