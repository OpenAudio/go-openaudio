package server

import (
	"context"
	"crypto/ecdsa"
	"os"
	"strings"
	"testing"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// The genesis writer inserts blocks straight into postgres and never runs them
// through ABCI, so anything FinalizeBlock would have written has to be written
// here instead. A test that counts transactions cannot tell the difference —
// the transactions are present either way. These assert ROWS.
func setupRewardStateTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dbURL := os.Getenv("TEST_DB_URL")
	if dbURL == "" {
		t.Skip("TEST_DB_URL not set, skipping database tests")
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS core_reward_pools (
			rewards_manager_pubkey text primary key,
			authorities            text[] not null default '{}',
			created_at             timestamptz default now(),
			updated_at             timestamptz default now()
		);
		CREATE TABLE IF NOT EXISTS core_rewards (
			id                     bigserial primary key,
			address                text not null,
			tx_hash                text not null,
			index                  bigint not null,
			sender                 text not null,
			reward_id              text not null,
			name                   text not null,
			amount                 bigint not null,
			raw_message            bytea,
			block_height           bigint not null,
			created_at             timestamptz default now(),
			updated_at             timestamptz default now(),
			rewards_manager_pubkey text
		);
		TRUNCATE core_reward_pools, core_rewards;
	`)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `TRUNCATE core_reward_pools, core_rewards`)
		pool.Close()
	})
	return pool
}

func signedRewardPoolTx(t *testing.T, key *ecdsa.PrivateKey, rm string, authorities []string) *corev1.SignedTransaction {
	t.Helper()
	body := &corev1.RewardPoolBody{
		DeadlineBlockHeight: 1_000_000,
		Action: &corev1.RewardPoolBody_Create{
			Create: &corev1.CreateRewardPool{RewardsManagerPubkey: rm, Authorities: authorities},
		},
	}
	sig, err := common.ProtoSign(key, body)
	require.NoError(t, err)
	return &corev1.SignedTransaction{
		Transaction: &corev1.SignedTransaction_RewardPool{
			RewardPool: &corev1.RewardPoolMessage{Body: body, Signature: sig},
		},
	}
}

func signedRewardTx(t *testing.T, key *ecdsa.PrivateKey, rm, rewardID, name string, amount uint64) *corev1.SignedTransaction {
	t.Helper()
	body := &corev1.RewardBody{
		DeadlineBlockHeight: 1_000_000,
		Action: &corev1.RewardBody_Create{
			Create: &corev1.CreateReward{
				RewardId:             rewardID,
				Name:                 name,
				Amount:               amount,
				RewardsManagerPubkey: rm,
			},
		},
	}
	sig, err := common.ProtoSign(key, body)
	require.NoError(t, err)
	return &corev1.SignedTransaction{
		Transaction: &corev1.SignedTransaction_Reward{
			Reward: &corev1.RewardMessage{Body: body, Signature: sig},
		},
	}
}

// Without this projection a migrated chain holds reward transactions whose
// state never materializes, and nothing downstream repairs it: the bootstrap
// node treats the blocks as already committed, and other nodes state-sync from
// its tables. So the pool and reward rows must exist after projection.
func TestProjectMigrationRewardStateWritesRows(t *testing.T) {
	pool := setupRewardStateTestDB(t)
	ctx := context.Background()
	q := db.New(pool)

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	authority := crypto.PubkeyToAddress(key.PublicKey).Hex()

	const (
		rm       = "HRRe6fbSDudpsBmkfBnLNHQnKkKgvhVc4pdBfR9U1YQz"
		chainID  = "test-chain"
		txhashRM = "aa11"
		txhashRW = "bb22"
	)

	handled, err := ProjectMigrationRewardState(ctx, q,
		signedRewardPoolTx(t, key, rm, []string{authority}), chainID, 7, 0, txhashRM)
	require.NoError(t, err)
	require.True(t, handled, "a reward pool transaction must be handled")

	handled, err = ProjectMigrationRewardState(ctx, q,
		signedRewardTx(t, key, rm, "code-1", "Launchpad Reward code-1", 42), chainID, 7, 1, txhashRW)
	require.NoError(t, err)
	require.True(t, handled, "a reward transaction must be handled")

	var pools int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM core_reward_pools`).Scan(&pools))
	require.Equal(t, 1, pools, "the pool row must exist; transactions alone are not migrated state")

	var (
		gotRM, gotSender, gotRewardID, gotName, gotAddress string
		gotAmount, gotHeight, gotIndex                     int64
	)
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT rewards_manager_pubkey, sender, reward_id, name, address, amount, block_height, index
		FROM core_rewards`).
		Scan(&gotRM, &gotSender, &gotRewardID, &gotName, &gotAddress, &gotAmount, &gotHeight, &gotIndex))

	require.Equal(t, rm, gotRM)
	require.Equal(t, "code-1", gotRewardID)
	require.Equal(t, "Launchpad Reward code-1", gotName)
	require.EqualValues(t, 42, gotAmount)
	require.EqualValues(t, 7, gotHeight)
	require.EqualValues(t, 1, gotIndex)

	// sender is API-visible, so it must be the address that actually signed.
	require.True(t, strings.EqualFold(authority, gotSender),
		"sender %s should be the signing authority %s", gotSender, authority)

	// address is derived from (txhash, chain id, height, message index), which
	// is why it can only be computed at block-write time.
	hashBytes, err := common.HexToBytes(txhashRW)
	require.NoError(t, err)
	require.Equal(t, common.CreateAddress(hashBytes, chainID, 7, 1, ""), gotAddress)
}

// The same reward at a different height or index is a different address, which
// is the property that makes computing it at synthesis time wrong.
func TestProjectedRewardAddressDependsOnBlockPosition(t *testing.T) {
	pool := setupRewardStateTestDB(t)
	ctx := context.Background()
	q := db.New(pool)

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	const rm = "HRRe6fbSDudpsBmkfBnLNHQnKkKgvhVc4pdBfR9U1YQz"

	_, err = ProjectMigrationRewardState(ctx, q,
		signedRewardPoolTx(t, key, rm, []string{crypto.PubkeyToAddress(key.PublicKey).Hex()}), "c", 1, 0, "aa11")
	require.NoError(t, err)

	for i, pos := range []struct{ height, index int64 }{{1, 0}, {2, 0}, {2, 1}} {
		tx := signedRewardTx(t, key, rm, "code-"+string(rune('a'+i)), "n", 1)
		_, err := ProjectMigrationRewardState(ctx, q, tx, "c", pos.height, pos.index, "bb22")
		require.NoError(t, err)
	}

	var distinct int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(DISTINCT address) FROM core_rewards`).Scan(&distinct))
	require.Equal(t, 3, distinct, "block position must be part of the reward address")
}

// Non-reward transactions must pass through untouched so the caller can hand it
// every transaction in a block without pre-filtering.
func TestProjectMigrationRewardStateIgnoresOtherTransactions(t *testing.T) {
	pool := setupRewardStateTestDB(t)
	ctx := context.Background()
	q := db.New(pool)

	handled, err := ProjectMigrationRewardState(ctx, q,
		&corev1.SignedTransaction{Transaction: &corev1.SignedTransaction_Plays{Plays: &corev1.TrackPlays{}}},
		"c", 1, 0, "aa11")
	require.NoError(t, err)
	require.False(t, handled, "a plays transaction is not reward state")

	var rows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM core_rewards`).Scan(&rows))
	require.Zero(t, rows)
}
