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
	"github.com/OpenAudio/go-openaudio/pkg/core/server"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mr-tron/base58/base58"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// rewardTestChainID keeps this test's blocks and transactions out of the way
// of anything else sharing the database.
const rewardTestChainID = "genesis-writer-reward-projection-test"

// rewardTestHeight is the block height these tests write to. High enough to sit
// clear of anything a writer test produces.
const rewardTestHeight = int64(9_000_001)

// legacyRewardDeadline is the deadline on the pre-rollout fixtures, distinct
// from convertedRewardDeadlineHeight so a conversion that forwarded the old
// value instead of setting its own would show up.
const legacyRewardDeadline = int64(1 << 40)

// TestRewardConversionAndProjection_MaterializesRowsNotJustTransactions is the
// test that fails without the projection, and now also without the conversion.
//
// The genesis writer bypasses ABCI, so a replayed reward reaches postgres as a
// row in core_transactions and nothing more: FinalizeBlock, which is what
// normally writes core_reward_pools / core_rewards, never runs. The assertions
// are split to keep the distinction visible — the transaction count passes
// either way, and the row counts are what the projection buys.
//
// The reward going in is legacy-shaped and signed by the pre-rotation
// authority, which is the production shape for 442 of the 471 rewards. What
// comes out must be a modern transaction signed by the pool's CURRENT
// authority, and the row's sender must be that current authority rather than
// the key the rotation removed.
func TestRewardConversionAndProjection_MaterializesRowsNotJustTransactions(t *testing.T) {
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

	f := newRewardFixture(t)

	exec(`DELETE FROM core_rewards WHERE block_height = $1`, rewardTestHeight)
	exec(`DELETE FROM core_reward_pools WHERE rewards_manager_pubkey = $1`, f.poolRM)
	exec(`DELETE FROM core_transactions WHERE block_id = $1`, rewardTestHeight)
	exec(`DELETE FROM core_app_state WHERE block_height = $1`, rewardTestHeight)
	exec(`DELETE FROM core_blocks WHERE height = $1 AND chain_id = $2`, rewardTestHeight, rewardTestChainID)

	w := f.writer(dst)

	// ---- convert, exactly as the DSN path does ------------------------------
	poolTxs, err := w.synthesizeRewardPoolTxs(f.pools)
	if err != nil {
		t.Fatalf("synthesize reward pool txs: %v", err)
	}
	legacyIn := scannedRewardTx{
		txBytes:     legacyRewardTx(t, f.legacyAuthority, "legacy-reward", "Legacy Reward", 111),
		txHash:      "AAAA01",
		blockHeight: 500,
	}
	modernIn := scannedRewardTx{
		txBytes:     modernRewardTx(t, f.currentAuthority, f.poolRM, "modern-reward", "Modern Reward", 222),
		txHash:      "AAAA02",
		blockHeight: 501,
	}
	bindings := map[rewardBindingKey]string{
		{txHash: "AAAA01", blockHeight: 500}: f.poolRM,
		{txHash: "AAAA02", blockHeight: 501}: f.poolRM,
	}

	rewardOut, stats, err := w.convertRewardTxs([]scannedRewardTx{legacyIn, modernIn}, f.pools, bindings)
	if err != nil {
		t.Fatalf("convertRewardTxs: %v", err)
	}
	if stats.converted != 1 || stats.alreadyModern != 1 || stats.droppedNoBinding != 0 {
		t.Fatalf("conversion stats = %+v, want 1 converted, 1 already modern, 0 dropped", stats)
	}

	// ---- no legacy reward artifacts leave the writer -------------------------
	for i, raw := range rewardOut {
		var stx corev1.SignedTransaction
		if err := proto.Unmarshal(raw, &stx); err != nil {
			t.Fatalf("unmarshal emitted reward %d: %v", i, err)
		}
		envelope := stx.GetReward()
		if envelope == nil {
			t.Fatalf("emitted reward %d is not a reward message", i)
		}
		if envelope.GetBody() == nil {
			t.Errorf("emitted reward %d has no body — it is still legacy-shaped", i)
		}
		legacy, err := server.TryParseLegacyReward(envelope)
		if err != nil {
			t.Fatalf("legacy probe on emitted reward %d: %v", i, err)
		}
		if legacy != nil {
			t.Errorf("emitted reward %d still parses as a legacy reward message", i)
		}
	}

	// ---- write the block through the real path -------------------------------
	txBytes := append(append([][]byte{}, poolTxs...), rewardOut...)
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
		`SELECT authorities FROM core_reward_pools WHERE rewards_manager_pubkey = $1`, f.poolRM).
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
		wantAmount int64
	}{
		{"legacy-reward", 111},
		{"modern-reward", 222},
	} {
		r, ok := got[tc.id]
		if !ok {
			t.Errorf("no core_rewards row for %s", tc.id)
			continue
		}
		// Both resolve to the pool's CURRENT authority. For the converted one
		// that is the point: the old chain records the pre-rotation key here.
		if !strings.EqualFold(r.sender, f.currentAddr) {
			t.Errorf("%s sender = %s, want the pool's current authority %s", tc.id, r.sender, f.currentAddr)
		}
		if r.amount != tc.wantAmount {
			t.Errorf("%s amount = %d, want %d", tc.id, r.amount, tc.wantAmount)
		}
		if r.rm == nil || *r.rm != f.poolRM {
			t.Errorf("%s rewards_manager_pubkey = %v, want %s", tc.id, r.rm, f.poolRM)
		}
	}

	// ---- the rotation survived the replay ------------------------------------
	// finalizeLegacyCreateReward unions a legacy reward's inline authorities
	// into its pool, which on a from-genesis block sync is the only thing that
	// materializes the pool at all. Conversion drops those authorities outright
	// and the writer creates the pool at its end state, so the pre-rotation key
	// must be nowhere in sight.
	if len(authorities) != 1 || !strings.EqualFold(authorities[0], f.currentAddr) {
		t.Errorf("pool authorities = %v, want exactly [%s]; the pre-rotation authority came back into the pool",
			authorities, strings.ToLower(f.currentAddr))
	}
}

// TestConvertRewardTxs covers the conversion in isolation: signer, pool
// binding, deadline, passthrough and the drop case.
func TestConvertRewardTxs(t *testing.T) {
	f := newRewardFixture(t)
	w := f.writer(nil)

	legacyIn := scannedRewardTx{
		txBytes:     legacyRewardTx(t, f.legacyAuthority, "r-legacy", "Legacy", 10),
		txHash:      "BBBB01",
		blockHeight: 700,
	}
	modernRaw := modernRewardTx(t, f.currentAuthority, f.poolRM, "r-modern", "Modern", 20)
	modernIn := scannedRewardTx{txBytes: modernRaw, txHash: "BBBB02", blockHeight: 701}
	orphan := scannedRewardTx{
		txBytes:     legacyRewardTx(t, f.legacyAuthority, "r-orphan", "Orphan", 30),
		txHash:      "BBBB03",
		blockHeight: 702,
	}

	bindings := map[rewardBindingKey]string{
		{txHash: "BBBB01", blockHeight: 700}: f.poolRM,
		{txHash: "BBBB02", blockHeight: 701}: f.poolRM,
		// BBBB03 deliberately absent: the old chain has no reward row for it.
	}

	out, stats, err := w.convertRewardTxs([]scannedRewardTx{legacyIn, modernIn, orphan}, f.pools, bindings)
	if err != nil {
		t.Fatalf("convertRewardTxs: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("emitted %d rewards, want 2", len(out))
	}
	if stats.converted != 1 || stats.alreadyModern != 1 || stats.droppedNoBinding != 1 {
		t.Fatalf("stats = %+v, want 1/1/1", stats)
	}

	// The already-modern transaction is passed through untouched, so its hash
	// still matches the old chain.
	if string(out[1]) != string(modernRaw) {
		t.Error("the already-modern reward was rewritten; it should pass through byte-for-byte")
	}

	// The converted one is signed by the pool's current authority.
	var stx corev1.SignedTransaction
	if err := proto.Unmarshal(out[0], &stx); err != nil {
		t.Fatalf("unmarshal converted reward: %v", err)
	}
	body := stx.GetReward().GetBody()
	if body == nil {
		t.Fatal("converted reward has no body")
	}
	if got := body.GetDeadlineBlockHeight(); got != convertedRewardDeadlineHeight {
		t.Errorf("converted deadline = %d, want %d (a forwarded legacy deadline would be %d)",
			got, convertedRewardDeadlineHeight, legacyRewardDeadline)
	}
	create := body.GetCreate()
	if create == nil {
		t.Fatal("converted reward is not a CreateReward")
	}
	if create.GetRewardsManagerPubkey() != f.poolRM {
		t.Errorf("converted RM = %q, want %q", create.GetRewardsManagerPubkey(), f.poolRM)
	}
	if create.GetRewardId() != "r-legacy" || create.GetName() != "Legacy" || create.GetAmount() != 10 {
		t.Errorf("converted fields = (%q, %q, %d), want (r-legacy, Legacy, 10)",
			create.GetRewardId(), create.GetName(), create.GetAmount())
	}
	signer, err := common.ProtoRecover(body, stx.GetReward().GetSignature())
	if err != nil {
		t.Fatalf("recover converted signer: %v", err)
	}
	if !strings.EqualFold(signer, f.currentAddr) {
		t.Errorf("converted reward signer = %s, want the pool's current authority %s", signer, f.currentAddr)
	}
}

// TestConvertRewardTxs_MissingKeyFails pins the fail-loud behaviour: a pool
// with no supplied key stops the run instead of silently falling back to the
// legacy bytes.
func TestConvertRewardTxs_MissingKeyFails(t *testing.T) {
	f := newRewardFixture(t)
	w := f.writer(nil)
	w.cfg.RewardSigningKeys = rewardSigningKeys{} // operator supplied nothing

	if missing := w.missingPoolSigners(f.pools); len(missing) != 1 {
		t.Fatalf("missingPoolSigners returned %d pools, want 1", len(missing))
	}
	if _, err := w.synthesizeRewardPoolTxs(f.pools); err == nil {
		t.Error("synthesizing pool creates without a key succeeded, want an error")
	}
	in := []scannedRewardTx{{
		txBytes:     legacyRewardTx(t, f.legacyAuthority, "r", "R", 1),
		txHash:      "CCCC01",
		blockHeight: 800,
	}}
	bindings := map[rewardBindingKey]string{{txHash: "CCCC01", blockHeight: 800}: f.poolRM}
	if _, _, err := w.convertRewardTxs(in, f.pools, bindings); err == nil {
		t.Error("converting without a key succeeded, want an error")
	}
}

// TestSynthesizeRewardPoolTxs covers the shape of the pool transactions the
// writer invents.
func TestSynthesizeRewardPoolTxs(t *testing.T) {
	f := newRewardFixture(t)
	w := f.writer(nil)

	txs, err := w.synthesizeRewardPoolTxs(f.pools)
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if len(txs) != 1 {
		t.Fatalf("got %d txs, want 1", len(txs))
	}

	var stx corev1.SignedTransaction
	if err := proto.Unmarshal(txs[0], &stx); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	envelope := stx.GetRewardPool()
	if envelope == nil {
		t.Fatal("not a reward pool transaction")
	}
	create := envelope.GetBody().GetCreate()
	if create == nil {
		t.Fatal("not a CreateRewardPool")
	}
	if create.GetRewardsManagerPubkey() != f.poolRM {
		t.Errorf("RM = %q, want %q", create.GetRewardsManagerPubkey(), f.poolRM)
	}

	// The envelope signature is real: validateCreateRewardPool requires the
	// recovered signer to be one of the initial authorities, and it is.
	signer, err := common.ProtoRecover(envelope.GetBody(), envelope.GetSignature())
	if err != nil {
		t.Fatalf("recover pool create signer: %v", err)
	}
	if !strings.EqualFold(signer, f.currentAddr) {
		t.Errorf("pool create signer = %s, want an initial authority %s", signer, f.currentAddr)
	}

	// rm_owner_signature cannot be produced without the launchpad's ed25519 RM
	// secret. Asserted empty so nobody fills it with something forged.
	if len(envelope.GetRmOwnerSignature()) != 0 {
		t.Error("pool create carries an rm_owner_signature; it cannot be produced honestly and must stay empty")
	}
	if envelope.GetBody().GetDeadlineBlockHeight() != convertedRewardDeadlineHeight {
		t.Errorf("pool create deadline = %d, want %d", envelope.GetBody().GetDeadlineBlockHeight(), convertedRewardDeadlineHeight)
	}

	// A pool with no authority can never attest, so migrating one is a bug
	// worth failing on rather than a row worth copying.
	if _, err := w.synthesizeRewardPoolTxs([]rewardPool{{RewardsManagerPubkey: "RM-empty"}}); err == nil {
		t.Error("synthesizing a pool with no authorities succeeded, want an error")
	}
}

// TestLoadRewardSigningKeys covers the key input, including the transposition
// guard that would otherwise sign one pool's rewards with another pool's key.
func TestLoadRewardSigningKeys(t *testing.T) {
	key := mustGenKey(t)
	addr := crypto.PubkeyToAddress(key.PublicKey).Hex()
	priv := hex.EncodeToString(crypto.FromECDSA(key))

	t.Setenv(rewardSigningKeysEnvVar, `{"`+addr+`":"`+priv+`"}`)
	keys, err := loadRewardSigningKeys("")
	if err != nil {
		t.Fatalf("load from env: %v", err)
	}
	if _, ok := keys[strings.ToLower(addr)]; !ok {
		t.Errorf("key for %s not loaded; got %v", addr, keys.authorityAddresses())
	}

	other := crypto.PubkeyToAddress(mustGenKey(t).PublicKey).Hex()
	t.Setenv(rewardSigningKeysEnvVar, `{"`+other+`":"`+priv+`"}`)
	if _, err := loadRewardSigningKeys(""); err == nil {
		t.Error("a key listed under the wrong address loaded successfully, want an error")
	}

	t.Setenv(rewardSigningKeysEnvVar, "")
	keys, err = loadRewardSigningKeys("")
	if err != nil || keys != nil {
		t.Errorf("unset env = (%v, %v), want (nil, nil)", keys, err)
	}
}

// ---- fixtures --------------------------------------------------------------

// rewardFixture is one pool that has been rotated off its launchpad key, plus
// the keys for both sides of that rotation.
type rewardFixture struct {
	currentAuthority *ecdsa.PrivateKey
	legacyAuthority  *ecdsa.PrivateKey
	currentAddr      string
	legacyAddr       string
	poolRM           string
	pools            []rewardPool
	keys             rewardSigningKeys
}

func newRewardFixture(t *testing.T) *rewardFixture {
	t.Helper()
	current := mustGenKey(t)
	legacy := mustGenKey(t)
	currentAddr := crypto.PubkeyToAddress(current.PublicKey).Hex()

	f := &rewardFixture{
		currentAuthority: current,
		legacyAuthority:  legacy,
		currentAddr:      currentAddr,
		legacyAddr:       crypto.PubkeyToAddress(legacy.PublicKey).Hex(),
		// A well-formed reward manager pubkey: 32 bytes, base58.
		poolRM: base58.Encode(crypto.Keccak256([]byte("reward-projection-test-rm"))),
		keys:   rewardSigningKeys{strings.ToLower(currentAddr): current},
	}
	f.pools = []rewardPool{{
		RewardsManagerPubkey: f.poolRM,
		Authorities:          []string{strings.ToLower(currentAddr)},
	}}
	return f
}

func (f *rewardFixture) writer(dst *pgxpool.Pool) *Writer {
	return &Writer{
		cfg: &WriterConfig{
			ChainID:           rewardTestChainID,
			MaxTxsPerBlock:    1 << 20,
			RewardSigningKeys: f.keys,
		},
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
		DeadlineBlockHeight: legacyRewardDeadline,
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
		DeadlineBlockHeight: convertedRewardDeadlineHeight,
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
