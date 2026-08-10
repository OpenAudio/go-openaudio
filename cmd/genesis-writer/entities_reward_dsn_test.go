package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// The scan finds reward transactions by searching for their protobuf wire tag
// in the raw bytes. Get a tag wrong and the scan silently finds nothing — no
// error, no rejection, just a migration missing every reward. So the constants
// are derived here from the generated types rather than trusted.
//
// This also fails if the field numbers change in types.proto, which is the
// realistic way it would break.
func TestRewardWireTagsMatchProto(t *testing.T) {
	for _, tc := range []struct {
		name string
		tx   *corev1.SignedTransaction
		want []byte
	}{
		{
			name: "reward",
			tx: &corev1.SignedTransaction{
				Transaction: &corev1.SignedTransaction_Reward{Reward: &corev1.RewardMessage{}},
			},
			want: rewardTag,
		},
		{
			name: "reward_pool",
			tx: &corev1.SignedTransaction{
				Transaction: &corev1.SignedTransaction_RewardPool{RewardPool: &corev1.RewardPoolMessage{}},
			},
			want: rewardPoolTag,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := proto.Marshal(tc.tx)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !bytes.Contains(b, tc.want) {
				t.Errorf("marshalled %s does not contain tag %x; encoding is %x — "+
					"the scan would find nothing", tc.name, tc.want, b)
			}
		})
	}
}

// The tags have to tell the two reward kinds apart, and neither may match a
// transaction type the migration must not replay.
func TestRewardWireTagsDoNotCollide(t *testing.T) {
	if bytes.Equal(rewardTag, rewardPoolTag) {
		t.Fatal("reward and reward_pool share a tag; the scan cannot distinguish them")
	}

	others := map[string]*corev1.SignedTransaction{
		"manage_entity_migration": {Transaction: &corev1.SignedTransaction_ManageEntityMigration{
			ManageEntityMigration: &corev1.ManageEntityLegacyMigration{}}},
		"manage_entity": {Transaction: &corev1.SignedTransaction_ManageEntity{
			ManageEntity: &corev1.ManageEntityLegacy{}}},
		"plays": {Transaction: &corev1.SignedTransaction_Plays{
			Plays: &corev1.TrackPlays{}}},
	}
	for name, tx := range others {
		b, err := proto.Marshal(tx)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		for tagName, tag := range map[string][]byte{"reward": rewardTag, "reward_pool": rewardPoolTag} {
			if bytes.Contains(b, tag) {
				t.Errorf("%s carries the %s tag %x; the scan would replay it as a reward",
					name, tagName, tag)
			}
		}
	}
}

// The tags cannot appear inside SignedTransaction's own string fields.
//
// proto3 requires `string` fields to be valid UTF-8, and both tags are
// invalid UTF-8 (0x8a and 0x9a are continuation bytes with no lead byte). So
// `signature` and `request_id` — the two fields serialized ahead of the oneof,
// and the obvious places a stray byte pair might collide — provably cannot
// produce a false positive.
//
// A collision remains possible inside a nested `bytes` field of some other
// transaction type, which is why the scan still confirms by type rather than
// trusting the match. This test records why that residual risk is small.
func TestRewardTagsCannotOccurInProtoStringFields(t *testing.T) {
	for name, tag := range map[string][]byte{"reward": rewardTag, "reward_pool": rewardPoolTag} {
		if utf8.Valid(tag) {
			t.Errorf("%s tag %x is valid UTF-8, so it could occur inside a signature "+
				"or request_id and produce false positives", name, tag)
		}
	}

	// Demonstrate it: the proto runtime refuses to encode such a string.
	_, err := proto.Marshal(&corev1.SignedTransaction{
		Signature:   string(rewardTag) + "not-a-reward",
		Transaction: &corev1.SignedTransaction_Plays{Plays: &corev1.TrackPlays{}},
	})
	if err == nil {
		t.Error("expected proto to reject a signature containing the tag bytes; " +
			"if it now accepts them, the false-positive analysis above no longer holds")
	}
}

// Rewards are the one migrated entity sourced from the old chain rather than
// the DP snapshot, so a missing source produces a chain with no reward history
// and no error. That happened: a full run completed cleanly, and the omission
// surfaced only when someone counted rows.
//
// --skip-rewards is the way to say "no rewards on purpose". Reaching this step
// without a source therefore means the flag was forgotten, not that rewards
// were unwanted.
func TestRewardsRequireASource(t *testing.T) {
	w := &Writer{cfg: &WriterConfig{}, logger: zap.NewNop()}

	err := w.writeRewards(context.Background())
	if err == nil {
		t.Fatal("writing rewards with neither --core-dsn nor --core-cmt-home " +
			"returned nil; a run with no reward source must not look successful")
	}
	for _, want := range []string{"--core-dsn", "--core-cmt-home", "--skip-rewards"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s; it should name every way out", err, want)
		}
	}
}
