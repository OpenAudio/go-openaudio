package mirror

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"testing"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// recoverEnvelopeSigner recovers a Plays envelope's signer the way core's
// mempool does (core/server/plays.go recoverPlaysSigner).
func recoverEnvelopeSigner(t *testing.T, tx *corev1.SignedTransaction) string {
	t.Helper()
	sig, err := hex.DecodeString(tx.GetSignature())
	require.NoError(t, err)
	body, err := proto.Marshal(tx.GetPlays())
	require.NoError(t, err)
	pub, err := crypto.SigToPub(accounts.TextHash(crypto.Keccak256(body)), sig)
	require.NoError(t, err)
	return crypto.PubkeyToAddress(*pub).Hex()
}

func newResigningMirror(t *testing.T, src *fakeSource, dst *fakeTarget, key *ecdsa.PrivateKey, skip ...string) *Mirror {
	t.Helper()
	m := New(Config{
		SourceChainID: srcChain, TargetChainID: dstChain, StartHeight: 10,
		MaxInflight: 50, CommitTimeoutBlocks: 3, PlaySigner: key, SkipTx: skip,
	}, src, dst, &memCursor{}, zap.NewNop())
	m.cfg.CursorSaveInterval = -1
	require.NoError(t, m.Init(context.Background()))
	return m
}

func resigned(t *testing.T, tx *corev1.SignedTransaction, key *ecdsa.PrivateKey) *corev1.SignedTransaction {
	t.Helper()
	c := proto.Clone(tx).(*corev1.SignedTransaction)
	require.NoError(t, resignPlays(c, key))
	return c
}

func TestResignsPlaysEnvelopesOnly(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	p, me := plays(1), manageEntity(1, "Create")
	p.Signature = "deadbeef" // the recording node's signature
	src := &fakeSource{chainID: srcChain, tip: 10, blocks: map[int64][][]byte{10: {raw(t, p), raw(t, me)}}}
	dst := newFakeTarget()

	m := newResigningMirror(t, src, dst, key)
	require.NoError(t, drive(t, m, src, dst))

	want := resigned(t, p, key)
	require.Equal(t, []string{hashOf(t, want), hashOf(t, me)}, dst.order,
		"the play is forwarded under its re-signed hash, the manage entity unchanged")
	got := dst.txs[hashOf(t, want)]
	require.Equal(t, crypto.PubkeyToAddress(key.PublicKey).Hex(), recoverEnvelopeSigner(t, got),
		"core recovers the mirror's key as the envelope signer")
	require.True(t, proto.Equal(p.GetPlays(), got.GetPlays()), "the plays themselves are unchanged")
	require.Zero(t, dst.forwards[hashOf(t, p)], "the source envelope is never sent")

	again := resigned(t, p, key)
	require.Equal(t, got.GetSignature(), again.GetSignature(), "signing is deterministic, so retries keep the hash")
}

// Plays mirrored before re-signing was turned on committed under their source
// envelope. Finding the play under that hash must count as done, or a restart
// across the switch would count it twice.
func TestResigningRecognisesPlaysCommittedUnderTheSourceHash(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	p := plays(1)
	src := &fakeSource{chainID: srcChain, tip: 10, blocks: map[int64][][]byte{10: {raw(t, p)}}}
	dst := newFakeTarget()
	dst.committed[hashOf(t, p)] = 50

	m := newResigningMirror(t, src, dst, key)
	require.NoError(t, drive(t, m, src, dst))

	require.Zero(t, dst.forwards[hashOf(t, resigned(t, p, key))], "a play already on target is not sent again")
	require.EqualValues(t, 1, m.Status().AlreadyOnTarget)
}

// An operator may name a play by either hash: the source one from the old
// chain, or the re-signed one a halt reports.
func TestResigningSkipsByEitherHash(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	a, b := plays(1), plays(2)
	src := &fakeSource{chainID: srcChain, tip: 10, blocks: map[int64][][]byte{10: {raw(t, a), raw(t, b)}}}
	dst := newFakeTarget()

	m := newResigningMirror(t, src, dst, key, hashOf(t, a), hashOf(t, resigned(t, b, key)))
	require.NoError(t, drive(t, m, src, dst))

	require.Empty(t, dst.order, "both plays skipped")
	require.Len(t, m.Status().Skipped, 2)
}
