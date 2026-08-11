package main

import (
	"crypto/ed25519"
	"strings"
	"testing"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/mr-tron/base58/base58"
	"google.golang.org/protobuf/proto"
)

// Two throwaway secrets standing in for a launchpad secret and its replacement.
const (
	testOriginalSecret = "1111111111111111111111111111111111111111111111111111111111111111"
	testRotatedSecret  = "2222222222222222222222222222222222222222222222222222222222222222"

	// mintRotated was launched before the rotation, so it has a reward manager
	// under each secret and the rotated one is a phantom.
	mintRotated = "So11111111111111111111111111111111111111112"
	// mintPostRotation was launched after, so its only reward manager derives
	// from the rotated secret and that one is real.
	mintPostRotation = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
)

func testKeys(t *testing.T) *launchpadKeys {
	t.Helper()
	t.Setenv(launchpadSecretEnvVar, testOriginalSecret)
	t.Setenv(launchpadRotatedSecretEnvVar, testRotatedSecret)
	t.Setenv(launchpadMintsEnvVar, mintRotated+","+mintPostRotation)

	keys, err := loadLaunchpadKeys("")
	if err != nil {
		t.Fatalf("load launchpad keys: %v", err)
	}
	if keys == nil {
		t.Fatal("expected keys with both secrets configured")
	}
	return keys
}

// rmAndAuthority returns the reward manager a (mint, generation) derives and
// the claim authority that goes with it.
func rmAndAuthority(t *testing.T, keys *launchpadKeys, mint string, gen secretGeneration) (string, string) {
	t.Helper()
	rm, ok := keys.rmForMint(mint, gen)
	if !ok {
		t.Fatalf("no %s-generation reward manager derived for mint %s", gen, mint)
	}
	id, ok := keys.identityForRM(rm)
	if !ok {
		t.Fatalf("reward manager %s is not indexed", rm)
	}
	return rm, id.authority
}

// A pool whose reward manager only derives from the rotated secret, for a mint
// that ALSO has an original-generation pool, names a Solana account that was
// never created — reward creation re-derived the manager after the secret
// changed. Emitting a create for it would make that dangling reference
// canonical on the new chain and strand its rewards, so it is dropped and its
// rewards move to the manager their mint actually has.
func TestPlanDropsPhantomPoolAndRemapsItsRewards(t *testing.T) {
	keys := testKeys(t)

	realRM, realAuth := rmAndAuthority(t, keys, mintRotated, secretOriginal)
	phantomRM, phantomAuth := rmAndAuthority(t, keys, mintRotated, secretRotated)

	// The real pool carries the post-rotation authority, matching what a
	// rotation leaves behind: the manager stays, the authority moves.
	pools := []rewardPool{
		{RewardsManagerPubkey: realRM, Authorities: []string{phantomAuth}},
		{RewardsManagerPubkey: phantomRM, Authorities: []string{phantomAuth}},
	}

	plan, err := planRewardPools(pools, keys)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if len(plan.pools) != 1 || plan.pools[0].RewardsManagerPubkey != realRM {
		t.Fatalf("expected only the real pool %s to be created, got %v", realRM, plan.pools)
	}
	if got := plan.remap[phantomRM]; got != realRM {
		t.Errorf("phantom %s remaps to %q, want the mint's real reward manager %s", phantomRM, got, realRM)
	}
	if len(plan.dropped) != 1 || plan.dropped[0] != phantomRM {
		t.Errorf("dropped = %v, want exactly [%s]", plan.dropped, phantomRM)
	}
	_ = realAuth
}

// A rotated-generation reward manager is not phantom by itself. A mint launched
// after the rotation has exactly one, it exists on Solana, and dropping it would
// silently discard that mint's rewards.
func TestPlanKeepsRotatedGenerationPoolForAPostRotationMint(t *testing.T) {
	keys := testKeys(t)

	rm, auth := rmAndAuthority(t, keys, mintPostRotation, secretRotated)
	plan, err := planRewardPools([]rewardPool{{RewardsManagerPubkey: rm, Authorities: []string{auth}}}, keys)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.pools) != 1 || plan.pools[0].RewardsManagerPubkey != rm {
		t.Fatalf("a post-rotation mint's only reward manager must be created; got %v", plan.pools)
	}
	if len(plan.dropped) != 0 {
		t.Errorf("dropped %v; nothing should be dropped when the mint has no original-generation pool", plan.dropped)
	}
}

// A pool nothing derives means the mint list is short or the secrets are wrong.
// Guessing would emit a create signed by a key that controls no reward manager.
func TestPlanFailsOnUnderivableRewardManager(t *testing.T) {
	keys := testKeys(t)

	_, err := planRewardPools([]rewardPool{{
		RewardsManagerPubkey: "HRRe6fbSDudpsBmkfBnLNHQnKkKgvhVc4pdBfR9U1YQz",
		Authorities:          []string{"0x0000000000000000000000000000000000000001"},
	}}, keys)
	if err == nil {
		t.Fatal("a pool no mint derives must stop the run")
	}
	if !strings.Contains(err.Error(), launchpadSecretEnvVar) {
		t.Errorf("error %q should name the inputs to fix", err)
	}
}

// Holding no key for any of a pool's authorities means every reward under it
// would be unsignable. Failing in the plan keeps that from surfacing halfway
// through emission, with some transactions already written.
func TestPlanFailsWhenNoAuthorityKeyIsHeld(t *testing.T) {
	keys := testKeys(t)

	rm, _ := rmAndAuthority(t, keys, mintPostRotation, secretRotated)
	_, err := planRewardPools([]rewardPool{{
		RewardsManagerPubkey: rm,
		Authorities:          []string{"0x000000000000000000000000000000000000dead"},
	}}, keys)
	if err == nil {
		t.Fatal("a pool whose authorities we hold no key for must stop the run")
	}
}

// The whole point of migrating these as real transactions: both signatures on a
// pool create must verify, independently, the way the validator checks them.
// rm_owner_signature is ed25519 by the reward manager keypair over the body, and
// the envelope is secp256k1 by a member of the pool's authorities.
func TestPoolCreateCarriesTwoVerifiableSignatures(t *testing.T) {
	keys := testKeys(t)
	w := &Writer{}

	rm, auth := rmAndAuthority(t, keys, mintPostRotation, secretRotated)
	pool := rewardPool{RewardsManagerPubkey: rm, Authorities: []string{auth}}

	txBytes, err := w.synthesizeRewardPoolTx(pool, keys)
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	var stx corev1.SignedTransaction
	if err := proto.Unmarshal(txBytes, &stx); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	envelope := stx.GetRewardPool()
	if envelope == nil {
		t.Fatal("transaction is not a reward pool message")
	}

	// rm_owner_signature: ed25519 against the reward manager pubkey itself.
	bodyBytes, err := common.ProtoSignableBytes(envelope.Body)
	if err != nil {
		t.Fatalf("signable bytes: %v", err)
	}
	rmPub, err := base58.Decode(rm)
	if err != nil {
		t.Fatalf("decode rm: %v", err)
	}
	if len(envelope.RmOwnerSignature) == 0 {
		t.Fatal("rm_owner_signature is empty; validateCreateRewardPool would reject this")
	}
	if !ed25519.Verify(ed25519.PublicKey(rmPub), bodyBytes, envelope.RmOwnerSignature) {
		t.Error("rm_owner_signature does not verify against the reward manager pubkey")
	}

	// envelope signature: recovered signer must be one of the pool authorities.
	signer, err := common.ProtoRecover(envelope.Body, envelope.Signature)
	if err != nil {
		t.Fatalf("recover envelope signer: %v", err)
	}
	if !strings.EqualFold(signer, auth) {
		t.Errorf("envelope signer %s is not the pool authority %s; the signer-membership check would reject it", signer, auth)
	}
}

// A reward must be signed by an authority of the pool it names, including after
// a remap moved it to a different pool.
func TestRewardIsSignedByItsPoolAuthorityAfterRemap(t *testing.T) {
	keys := testKeys(t)
	w := &Writer{}

	realRM, _ := rmAndAuthority(t, keys, mintRotated, secretOriginal)
	phantomRM, phantomAuth := rmAndAuthority(t, keys, mintRotated, secretRotated)

	plan, err := planRewardPools([]rewardPool{
		{RewardsManagerPubkey: realRM, Authorities: []string{phantomAuth}},
		{RewardsManagerPubkey: phantomRM, Authorities: []string{phantomAuth}},
	}, keys)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// A reward recorded against the phantom, as production has them.
	r := rewardRow{RewardID: "code-1", Name: "Launchpad Reward code-1", Amount: 42, RewardsManagerPubkey: phantomRM}
	target := plan.remap[r.RewardsManagerPubkey]
	if target != realRM {
		t.Fatalf("remap sent the reward to %q, want %s", target, realRM)
	}

	txBytes, err := w.synthesizeRewardTx(r, target, keys, plan)
	if err != nil {
		t.Fatalf("synthesize reward: %v", err)
	}
	var stx corev1.SignedTransaction
	if err := proto.Unmarshal(txBytes, &stx); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msg := stx.GetReward()
	if msg == nil {
		t.Fatal("transaction is not a reward message")
	}

	create := msg.Body.GetCreate()
	if create == nil {
		t.Fatal("reward body is not a create")
	}
	if create.RewardsManagerPubkey != realRM {
		t.Errorf("reward names %s, want the real reward manager %s", create.RewardsManagerPubkey, realRM)
	}
	if create.RewardId != r.RewardID || create.Name != r.Name || create.Amount != uint64(r.Amount) {
		t.Errorf("offer changed: got (%s, %s, %d), want (%s, %s, %d)",
			create.RewardId, create.Name, create.Amount, r.RewardID, r.Name, r.Amount)
	}

	signer, err := common.ProtoRecover(msg.Body, msg.Signature)
	if err != nil {
		t.Fatalf("recover reward signer: %v", err)
	}
	if !strings.EqualFold(signer, phantomAuth) {
		t.Errorf("reward signer %s is not an authority of pool %s", signer, realRM)
	}
}

// Emitting a reward for a pool that was never created would produce a chain the
// validator rejects at finalize time, which is far worse than stopping here.
func TestRewardForAnUncoveredPoolFails(t *testing.T) {
	keys := testKeys(t)
	w := &Writer{}

	_, err := w.synthesizeRewardTx(
		rewardRow{RewardID: "orphan", Amount: 1},
		"HRRe6fbSDudpsBmkfBnLNHQnKkKgvhVc4pdBfR9U1YQz",
		keys,
		&rewardPlan{remap: map[string]string{}},
	)
	if err == nil {
		t.Fatal("a reward naming a pool no plan covers must stop the run")
	}
}
