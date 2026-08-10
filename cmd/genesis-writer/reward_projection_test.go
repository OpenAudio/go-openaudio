package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	coredb "github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mr-tron/base58/base58"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// rewardTestChainID keeps this test's blocks and transactions out of the way
// of anything else sharing the database.
const rewardTestChainID = "genesis-writer-reward-projection-test"

// rewardTestDeadline is far above any height this test writes, so the deadline
// carried by the replayed reward messages is in the future.
const rewardTestDeadline = int64(1 << 40)

// TestRewardProjection_MaterializesRowsNotJustTransactions is the test that
// fails without the projection.
//
// The genesis writer bypasses ABCI, so a replayed reward reaches postgres as a
// row in core_transactions and nothing more: FinalizeBlock, which is what
// normally writes core_reward_pools / core_rewards, never runs. The assertions
// below are split to make the distinction visible — the transaction count
// passes either way, and the row counts are what the projection buys.
//
// The pool is created at its FINAL authority set, and the legacy reward
// replayed under it still carries the pre-rotation authority in its inline
// claim_authorities. That combination is the production shape (442 of the 471
// production rewards are in it) and it is why the last assertion matters: the
// pool must not absorb the old authority back out of the reward.
func TestRewardProjection_MaterializesRowsNotJustTransactions(t *testing.T) {
	dbURL := os.Getenv("ETL_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("ETL_TEST_DB_URL not set, skipping database test")
	}
	ctx := context.Background()
	logger := zap.NewNop()

	if err := coredb.RunMigrations(logger, dbURL, false); err != nil {
		t.Fatalf("run core migrations: %v", err)
	}
	dst, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect core db: %v", err)
	}
	defer dst.Close()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := dst.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}

	// ---- fixtures ------------------------------------------------------------
	// currentAuthority is where the pool ended up; legacyAuthority is the
	// launchpad-derived key it was rotated away from, and the key the legacy
	// reward is still signed by.
	currentAuthority := mustGenKey(t)
	legacyAuthority := mustGenKey(t)
	currentAddr := crypto.PubkeyToAddress(currentAuthority.PublicKey).Hex()
	legacyAddr := crypto.PubkeyToAddress(legacyAuthority.PublicKey).Hex()

	// A well-formed reward manager pubkey: 32 bytes, base58. Derived from the
	// test's own key so it cannot collide with a seeded production RM.
	rmBytes := crypto.Keccak256([]byte("reward-projection-test-rm"))
	poolRM := base58.Encode(rmBytes)

	exec(`DELETE FROM core_rewards WHERE tx_hash IN (
			SELECT tx_hash FROM core_transactions WHERE block_id = $1)`, rewardTestHeight)
	exec(`DELETE FROM core_reward_pools WHERE rewards_manager_pubkey = $1`, poolRM)
	exec(`DELETE FROM core_transactions WHERE block_id = $1`, rewardTestHeight)
	exec(`DELETE FROM core_app_state WHERE block_height = $1`, rewardTestHeight)
	exec(`DELETE FROM core_blocks WHERE height = $1 AND chain_id = $2`, rewardTestHeight, rewardTestChainID)
	exec(`DELETE FROM launchpad_authority_rm WHERE rewards_manager_pubkey = $1`, poolRM)

	// The launchpad seed is how a legacy reward finds its reward manager.
	exec(`INSERT INTO launchpad_authority_rm (authority, rewards_manager_pubkey) VALUES ($1, $2)
		ON CONFLICT (authority) DO UPDATE SET rewards_manager_pubkey = excluded.rewards_manager_pubkey`,
		strings.ToLower(legacyAddr), poolRM)

	w := newRewardTestWriter(t, dst)

	// ---- the transactions ----------------------------------------------------
	poolTxs, err := w.synthesizeRewardPoolTxs([]rewardPool{{
		RewardsManagerPubkey: poolRM,
		Authorities:          []string{strings.ToLower(currentAddr)},
	}})
	if err != nil {
		t.Fatalf("synthesize reward pool txs: %v", err)
	}
	if len(poolTxs) != 1 {
		t.Fatalf("synthesized %d pool txs, want 1", len(poolTxs))
	}

	legacyTx := legacyRewardTx(t, legacyAuthority, "legacy-reward", "Legacy Reward", 111)
	modernTx := modernRewardTx(t, currentAuthority, poolRM, "modern-reward", "Modern Reward", 222)

	txBytes := [][]byte{poolTxs[0], legacyTx, modernTx}
	pb := pendingBlock{
		height:    rewardTestHeight,
		blockTime: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		appHash:   []byte("reward-projection-test-app-hash"),
		hashHex:   hex.EncodeToString([]byte("reward-projection-test-block")),
	}
	for _, tx := range txBytes {
		pb.txData = append(pb.txData, txRow{
			hash:    strings.ToUpper(hex.EncodeToString(sha256Bytes(tx))),
			txBytes: tx,
		})
	}

	// The real write path: block header, transactions, app state, projection,
	// all in one postgres transaction.
	if err := w.writeBlockToDB(ctx, pb); err != nil {
		t.Fatalf("writeBlockToDB: %v", err)
	}
	if w.rewardProjectionSkips != 0 {
		t.Fatalf("reward projection declined %d transactions, want 0", w.rewardProjectionSkips)
	}

	// ---- the transactions landed (true with or without the projection) -------
	var txCount int
	if err := dst.QueryRow(ctx,
		`SELECT count(*) FROM core_transactions WHERE block_id = $1`, rewardTestHeight).
		Scan(&txCount); err != nil {
		t.Fatalf("count transactions: %v", err)
	}
	if txCount != len(txBytes) {
		t.Fatalf("core_transactions has %d rows, want %d", txCount, len(txBytes))
	}

	// ---- the rows landed (only true with the projection) ---------------------
	var authorities []string
	if err := dst.QueryRow(ctx,
		`SELECT authorities FROM core_reward_pools WHERE rewards_manager_pubkey = $1`, poolRM).
		Scan(&authorities); err != nil {
		t.Fatalf("the reward pool Create produced no core_reward_pools row: %v", err)
	}

	type rewardRowResult struct {
		sender string
		rm     *string
		amount int64
	}
	got := map[string]rewardRowResult{}
	rows, err := dst.Query(ctx,
		`SELECT reward_id, sender, rewards_manager_pubkey, amount
		 FROM core_rewards WHERE block_height = $1`, rewardTestHeight)
	if err != nil {
		t.Fatalf("query rewards: %v", err)
	}
	for rows.Next() {
		var id string
		var r rewardRowResult
		if err := rows.Scan(&id, &r.sender, &r.rm, &r.amount); err != nil {
			t.Fatalf("scan reward: %v", err)
		}
		got[id] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read rewards: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("core_rewards has %d rows for the replayed rewards, want 2 (transactions were written; the rows are the projection's job)", len(got))
	}
	for _, tc := range []struct {
		id         string
		wantSender string
		wantAmount int64
	}{
		{"legacy-reward", legacyAddr, 111},
		{"modern-reward", currentAddr, 222},
	} {
		r, ok := got[tc.id]
		if !ok {
			t.Errorf("no core_rewards row for %s", tc.id)
			continue
		}
		if !strings.EqualFold(r.sender, tc.wantSender) {
			t.Errorf("%s sender = %s, want %s", tc.id, r.sender, tc.wantSender)
		}
		if r.amount != tc.wantAmount {
			t.Errorf("%s amount = %d, want %d", tc.id, r.amount, tc.wantAmount)
		}
		if r.rm == nil || *r.rm != poolRM {
			t.Errorf("%s rewards_manager_pubkey = %v, want %s", tc.id, r.rm, poolRM)
		}
	}

	// ---- the rotation survived the replay ------------------------------------
	// finalizeLegacyCreateReward unions a legacy reward's inline authorities
	// into its pool, which on a from-genesis block sync is the only thing that
	// materializes the pool at all. The genesis writer has already created the
	// pool at its end state, so doing that here would re-grant attestation
	// authority to the key the rotation removed.
	if len(authorities) != 1 || !strings.EqualFold(authorities[0], currentAddr) {
		t.Errorf("pool authorities = %v, want exactly [%s]; the replayed legacy reward pulled its pre-rotation authority back into the pool",
			authorities, strings.ToLower(currentAddr))
	}
}

// rewardTestHeight is the block height this test writes to. High enough to sit
// clear of anything a writer test produces.
const rewardTestHeight = int64(9_000_001)

func newRewardTestWriter(t *testing.T, dst *pgxpool.Pool) *Writer {
	t.Helper()
	return &Writer{
		cfg:    &WriterConfig{ChainID: rewardTestChainID, MaxTxsPerBlock: 1 << 20},
		dstDB:  dst,
		logger: zap.NewNop(),
	}
}

func mustGenKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

// legacyRewardTx builds a pre-pool-rollout reward transaction: the action
// carries its own deadline and signature, and the envelope has no body. The
// bytes reach RewardMessage as unknown fields, which is exactly how they
// arrive off the old chain.
func legacyRewardTx(t *testing.T, key *ecdsa.PrivateKey, rewardID, name string, amount uint64) []byte {
	t.Helper()
	cr := &corev1.LegacyCreateReward{
		RewardId:            rewardID,
		Name:                name,
		Amount:              amount,
		DeadlineBlockHeight: rewardTestDeadline,
		ClaimAuthorities: []*corev1.ClaimAuthority{{
			Address: crypto.PubkeyToAddress(key.PublicKey).Hex(),
			Name:    "test-authority",
		}},
	}
	dataBytes, err := hex.DecodeString(common.LegacyDeterministicCreateRewardData(cr))
	if err != nil {
		t.Fatalf("decode legacy signing data: %v", err)
	}
	sig, err := common.EthSign(key, dataBytes)
	if err != nil {
		t.Fatalf("sign legacy reward: %v", err)
	}
	cr.Signature = sig

	raw, err := proto.Marshal(&corev1.LegacyRewardMessage{
		Action: &corev1.LegacyRewardMessage_Create{Create: cr},
	})
	if err != nil {
		t.Fatalf("marshal legacy reward message: %v", err)
	}
	var envelope corev1.RewardMessage
	if err := proto.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal legacy bytes as RewardMessage: %v", err)
	}
	if envelope.Body != nil {
		t.Fatal("legacy fixture decoded with a body; it is not legacy-shaped")
	}
	return mustMarshalSignedTx(t, &corev1.SignedTransaction{
		Transaction: &corev1.SignedTransaction_Reward{Reward: &envelope},
	})
}

// modernRewardTx builds a post-pool-rollout reward transaction: body plus
// envelope signature, referencing its pool by reward manager pubkey.
func modernRewardTx(t *testing.T, key *ecdsa.PrivateKey, poolRM, rewardID, name string, amount uint64) []byte {
	t.Helper()
	body := &corev1.RewardBody{
		DeadlineBlockHeight: rewardTestDeadline,
		Action: &corev1.RewardBody_Create{Create: &corev1.CreateReward{
			RewardId:             rewardID,
			Name:                 name,
			Amount:               amount,
			RewardsManagerPubkey: poolRM,
		}},
	}
	sig, err := common.ProtoSign(key, body)
	if err != nil {
		t.Fatalf("sign reward body: %v", err)
	}
	return mustMarshalSignedTx(t, &corev1.SignedTransaction{
		Transaction: &corev1.SignedTransaction_Reward{
			Reward: &corev1.RewardMessage{Body: body, Signature: sig},
		},
	})
}

func mustMarshalSignedTx(t *testing.T, stx *corev1.SignedTransaction) []byte {
	t.Helper()
	b, err := proto.Marshal(stx)
	if err != nil {
		t.Fatalf("marshal signed transaction: %v", err)
	}
	return b
}

// TestSynthesizeRewardPoolTxs covers the shape of the pool transactions the
// writer invents, independently of any database.
func TestSynthesizeRewardPoolTxs(t *testing.T) {
	w := &Writer{cfg: &WriterConfig{ChainID: rewardTestChainID}, logger: zap.NewNop()}

	pools := []rewardPool{
		{RewardsManagerPubkey: "RM-one", Authorities: []string{"0xaaa", "0xbbb"}},
		{RewardsManagerPubkey: "RM-two", Authorities: []string{"0xccc"}},
	}
	txs, err := w.synthesizeRewardPoolTxs(pools)
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if len(txs) != len(pools) {
		t.Fatalf("got %d txs, want %d", len(txs), len(pools))
	}

	for i, raw := range txs {
		var stx corev1.SignedTransaction
		if err := proto.Unmarshal(raw, &stx); err != nil {
			t.Fatalf("unmarshal tx %d: %v", i, err)
		}
		envelope := stx.GetRewardPool()
		if envelope == nil {
			t.Fatalf("tx %d is not a reward pool transaction", i)
		}
		create := envelope.GetBody().GetCreate()
		if create == nil {
			t.Fatalf("tx %d is not a CreateRewardPool", i)
		}
		if create.GetRewardsManagerPubkey() != pools[i].RewardsManagerPubkey {
			t.Errorf("tx %d RM = %q, want %q", i, create.GetRewardsManagerPubkey(), pools[i].RewardsManagerPubkey)
		}
		if len(create.GetAuthorities()) != len(pools[i].Authorities) {
			t.Errorf("tx %d authorities = %v, want %v", i, create.GetAuthorities(), pools[i].Authorities)
		}
		// Unsigned on purpose, and asserted so that "sign it later" cannot
		// happen without a decision. See synthesizeRewardPoolTxs.
		if envelope.GetSignature() != "" {
			t.Errorf("tx %d carries an envelope signature %q; pool creates are emitted unsigned", i, envelope.GetSignature())
		}
		if len(envelope.GetRmOwnerSignature()) != 0 {
			t.Errorf("tx %d carries an rm_owner_signature; pool creates are emitted unsigned", i)
		}
		if envelope.GetBody().GetDeadlineBlockHeight() != 0 {
			t.Errorf("tx %d carries a fabricated deadline %d", i, envelope.GetBody().GetDeadlineBlockHeight())
		}
	}

	// A pool with no authority can never attest, so migrating one is a bug
	// worth failing on rather than a row worth copying.
	if _, err := w.synthesizeRewardPoolTxs([]rewardPool{{RewardsManagerPubkey: "RM-empty"}}); err == nil {
		t.Error("synthesizing a pool with no authorities succeeded, want an error")
	}
}
