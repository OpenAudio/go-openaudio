package mirror

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

const (
	srcChain = "audius-mainnet-alpha-beta"
	dstChain = "audius-mainnet-beta"
)

func manageEntity(id int64, action string) *corev1.SignedTransaction {
	return &corev1.SignedTransaction{
		RequestId: fmt.Sprintf("me-%d-%s", id, action),
		Transaction: &corev1.SignedTransaction_ManageEntity{ManageEntity: &corev1.ManageEntityLegacy{
			UserId: id, EntityType: "Track", EntityId: id, Action: action, Nonce: fmt.Sprintf("0x%x-%s", id, action),
		}},
	}
}

func plays(n int) *corev1.SignedTransaction {
	return &corev1.SignedTransaction{
		RequestId: fmt.Sprintf("plays-%d", n),
		Transaction: &corev1.SignedTransaction_Plays{Plays: &corev1.TrackPlays{
			Plays: []*corev1.TrackPlay{{UserId: "u", TrackId: fmt.Sprint(n)}},
		}},
	}
}

// sla is a type the mirror must leave behind.
func sla(n int) *corev1.SignedTransaction {
	return &corev1.SignedTransaction{
		RequestId:   fmt.Sprintf("sla-%d", n),
		Transaction: &corev1.SignedTransaction_SlaRollup{SlaRollup: &corev1.SlaRollup{BlockStart: int64(n)}},
	}
}

func raw(t *testing.T, tx *corev1.SignedTransaction) []byte {
	t.Helper()
	b, err := proto.Marshal(tx)
	require.NoError(t, err)
	return b
}

func hashOf(t *testing.T, tx *corev1.SignedTransaction) string {
	return common.ToTxHashFromBytes(raw(t, tx))
}

type fakeSource struct {
	chainID string
	blocks  map[int64][][]byte
	tip     int64
}

func (s *fakeSource) ChainID(context.Context) (string, error) { return s.chainID, nil }
func (s *fakeSource) Tip(context.Context) (int64, error)      { return s.tip, nil }
func (s *fakeSource) Block(_ context.Context, h int64) (string, [][]byte, error) {
	if h > s.tip {
		return "", nil, fmt.Errorf("block %d beyond tip", h)
	}
	return s.chainID, s.blocks[h], nil
}

// fakeTarget is a single-proposer chain: Tick commits the mempool in order,
// skipping anything the proposer rejects.
type fakeTarget struct {
	chainID   string
	height    int64
	mempool   []string
	txs       map[string]*corev1.SignedTransaction
	committed map[string]int64
	order     []string
	// refuse is admission validation; it sees what is committed so far.
	refuse func(tx *corev1.SignedTransaction, committed map[string]int64) error
	// dropped transactions are admitted but never make it into a block.
	dropped  map[string]bool
	forwards map[string]int
	// firstForward is the height at which each hash was first submitted.
	firstForward map[string]int64
	// perBlock caps how many transactions one block takes; 0 is unlimited.
	perBlock int
}

func newFakeTarget() *fakeTarget {
	return &fakeTarget{
		chainID: dstChain, height: 100,
		txs: map[string]*corev1.SignedTransaction{}, committed: map[string]int64{},
		dropped: map[string]bool{}, forwards: map[string]int{}, firstForward: map[string]int64{},
	}
}

func (t *fakeTarget) Status(context.Context) (string, int64, error) { return t.chainID, t.height, nil }

func (t *fakeTarget) Forward(_ context.Context, tx *corev1.SignedTransaction) error {
	b, _ := proto.Marshal(tx)
	h := common.ToTxHashFromBytes(b)
	t.forwards[h]++
	if _, ok := t.firstForward[h]; !ok {
		t.firstForward[h] = t.height
	}
	if t.refuse != nil {
		if err := t.refuse(tx, t.committed); err != nil {
			return err
		}
	}
	if _, ok := t.committed[h]; ok {
		return nil
	}
	for _, m := range t.mempool {
		if m == h {
			return nil
		}
	}
	t.mempool = append(t.mempool, h)
	t.txs[h] = tx
	return nil
}

func (t *fakeTarget) Committed(_ context.Context, hash string) (int64, bool, error) {
	h, ok := t.committed[hash]
	return h, ok, nil
}

func (t *fakeTarget) Tick() {
	t.height++
	var left []string
	taken := 0
	for _, h := range t.mempool {
		if t.dropped[h] {
			continue
		}
		if t.perBlock > 0 && taken == t.perBlock {
			left = append(left, h)
			continue
		}
		t.committed[h] = t.height
		t.order = append(t.order, h)
		taken++
	}
	t.mempool = left
}

type memCursor struct {
	pos   Position
	saved bool
}

func (c *memCursor) Load() (Position, bool, error) { return c.pos, c.saved, nil }
func (c *memCursor) Save(p Position) error         { c.pos, c.saved = p, true; return nil }

func newMirror(t *testing.T, src *fakeSource, dst *fakeTarget, cur *memCursor, start int64) *Mirror {
	t.Helper()
	m := New(Config{
		SourceChainID: srcChain, TargetChainID: dstChain, StartHeight: start,
		MaxInflight: 50, CommitTimeoutBlocks: 3,
	}, src, dst, cur, zap.NewNop())
	m.cfg.CursorSaveInterval = -1 // save on every change, so tests see it
	require.NoError(t, m.Init(context.Background()))
	return m
}

// drive steps the mirror and ticks the target until the mirror has settled
// everything up to the source tip, or returns the error that stopped it.
func drive(t *testing.T, m *Mirror, src *fakeSource, dst *fakeTarget) error {
	t.Helper()
	ctx := context.Background()
	for range 200 {
		if _, err := m.Step(ctx); err != nil {
			var halt *HaltError
			if errors.As(err, &halt) {
				return err
			}
			t.Fatalf("unexpected step error: %v", err)
		}
		if m.settled.Height > src.tip && len(m.inflight) == 0 && !m.draining {
			return nil
		}
		dst.Tick()
	}
	t.Fatal("mirror did not settle")
	return nil
}

func TestMirrorsManageEntityAndPlaysInBlockOrder(t *testing.T) {
	a, b, p, c := manageEntity(1, "Create"), manageEntity(2, "Create"), plays(1), manageEntity(1, "Update")
	src := &fakeSource{chainID: srcChain, tip: 12, blocks: map[int64][][]byte{
		10: {raw(t, a), raw(t, sla(1)), raw(t, b)},
		11: {},
		12: {raw(t, p), raw(t, c)},
	}}
	dst := newFakeTarget()
	cur := &memCursor{}
	m := newMirror(t, src, dst, cur, 10)

	require.NoError(t, drive(t, m, src, dst))

	require.Equal(t, []string{hashOf(t, a), hashOf(t, b), hashOf(t, p), hashOf(t, c)}, dst.order,
		"mirrored in block order, SLA rollup left behind")
	require.Equal(t, Position{Height: 13}, cur.pos, "cursor past the tip")
	st := m.Status()
	require.EqualValues(t, 4, st.Submitted)
	require.EqualValues(t, 4, st.Committed)
	require.Zero(t, st.SplitBlocks)
}

// A block's transactions must all commit before the next block is read, so no
// target block mixes two source blocks. An indexer keys some rows on block
// number and block time, and re-sorts within a block, so mixing would change
// what it produces.
func TestFollowsTheSourceBlockByBlock(t *testing.T) {
	a, b, c := manageEntity(1, "Create"), plays(1), manageEntity(2, "Create")
	src := &fakeSource{chainID: srcChain, tip: 11, blocks: map[int64][][]byte{
		10: {raw(t, a), raw(t, b)},
		11: {raw(t, c)},
	}}
	dst := newFakeTarget()
	m := newMirror(t, src, dst, &memCursor{}, 10)
	require.NoError(t, drive(t, m, src, dst))

	lastOfBlock10 := max(dst.committed[hashOf(t, a)], dst.committed[hashOf(t, b)])
	require.Greater(t, dst.firstForward[hashOf(t, c)], lastOfBlock10-1,
		"block 11 was submitted before block 10 had committed")
	require.NotEqual(t, dst.committed[hashOf(t, a)], dst.committed[hashOf(t, c)], "blocks 10 and 11 share a target block")
	require.Zero(t, m.Status().SplitBlocks)
}

// The mirror cannot stop a proposal from starting partway through a block's
// submissions, so it reports the split instead.
func TestReportsASourceBlockSplitAcrossTargetBlocks(t *testing.T) {
	a, b, c := manageEntity(1, "Create"), plays(1), manageEntity(2, "Create")
	src := &fakeSource{chainID: srcChain, tip: 11, blocks: map[int64][][]byte{
		10: {raw(t, a), raw(t, b)},
		11: {raw(t, c)},
	}}
	dst := newFakeTarget()
	dst.perBlock = 1 // forces block 10's two transactions into two target blocks
	m := newMirror(t, src, dst, &memCursor{}, 10)
	require.NoError(t, drive(t, m, src, dst))

	st := m.Status()
	require.EqualValues(t, 1, st.SplitBlocks, "only block 10 was split")
	require.NotNil(t, st.LastSplit)
	require.EqualValues(t, 10, st.LastSplit.SourceHeight)
	require.Equal(t, []int64{dst.committed[hashOf(t, a)], dst.committed[hashOf(t, b)]}, st.LastSplit.TargetHeights)
}

// A restart re-reads from the saved cursor. Anything already committed in that
// range must not be sent again, or plays would be counted twice.
func TestSkipsWhatTheTargetAlreadyHas(t *testing.T) {
	a, p := manageEntity(1, "Create"), plays(1)
	src := &fakeSource{chainID: srcChain, tip: 10, blocks: map[int64][][]byte{10: {raw(t, a), raw(t, p)}}}
	dst := newFakeTarget()
	dst.committed[hashOf(t, p)] = 50 // committed before the crash, cursor not saved

	m := newMirror(t, src, dst, &memCursor{}, 10)
	require.NoError(t, drive(t, m, src, dst))

	require.Equal(t, 0, dst.forwards[hashOf(t, p)], "play already on target must not be resent")
	require.Equal(t, 1, dst.forwards[hashOf(t, a)])
	require.EqualValues(t, 1, m.Status().AlreadyOnTarget)
}

// Admission validates against committed state, so an update whose create is
// still in flight is refused. That is not a real refusal: drain and retry.
func TestDependencyRefusalDrainsAndRetries(t *testing.T) {
	create, update := manageEntity(7, "Create"), manageEntity(7, "Update")
	createHash := hashOf(t, create)
	src := &fakeSource{chainID: srcChain, tip: 10, blocks: map[int64][][]byte{10: {raw(t, create), raw(t, update)}}}
	dst := newFakeTarget()
	dst.refuse = func(tx *corev1.SignedTransaction, committed map[string]int64) error {
		if tx.GetManageEntity().GetAction() == "Update" {
			if _, ok := committed[createHash]; !ok {
				return connect.NewError(connect.CodeInvalidArgument, errors.New("entity does not exist"))
			}
		}
		return nil
	}

	m := newMirror(t, src, dst, &memCursor{}, 10)
	require.NoError(t, drive(t, m, src, dst))
	require.Equal(t, []string{createHash, hashOf(t, update)}, dst.order)
	require.Equal(t, 2, dst.forwards[hashOf(t, update)], "refused once while the create was in flight, then accepted")
}

// A refusal with nothing in flight is a real one. The mirror stops at it and
// never moves the cursor past it.
func TestRefusalWithNothingInFlightHalts(t *testing.T) {
	good, bad, after := manageEntity(1, "Create"), manageEntity(2, "Create"), manageEntity(3, "Create")
	src := &fakeSource{chainID: srcChain, tip: 11, blocks: map[int64][][]byte{
		10: {raw(t, good)},
		11: {raw(t, bad), raw(t, after)},
	}}
	dst := newFakeTarget()
	badHash := hashOf(t, bad)
	dst.refuse = func(tx *corev1.SignedTransaction, _ map[string]int64) error {
		if tx.GetManageEntity().GetEntityId() == 2 {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("not authorized"))
		}
		return nil
	}
	cur := &memCursor{}
	m := newMirror(t, src, dst, cur, 10)

	err := drive(t, m, src, dst)
	var halt *HaltError
	require.ErrorAs(t, err, &halt)
	require.Equal(t, badHash, halt.Hash)
	require.Equal(t, Position{Height: 11, Index: 0}, halt.Pos)
	require.Equal(t, Position{Height: 11, Index: 0}, cur.pos, "cursor stops at the refused tx")
	require.Equal(t, 0, dst.forwards[hashOf(t, after)], "nothing after the refusal is sent")
	require.Equal(t, []string{hashOf(t, good)}, dst.order)
}

// A proposer can drop an admitted transaction without telling anyone. The
// mirror resubmits once, then halts rather than carry on without it.
func TestDroppedTransactionResubmitsOnceThenHalts(t *testing.T) {
	a := manageEntity(1, "Create")
	src := &fakeSource{chainID: srcChain, tip: 10, blocks: map[int64][][]byte{10: {raw(t, a)}}}
	dst := newFakeTarget()
	dst.dropped[hashOf(t, a)] = true

	m := newMirror(t, src, dst, &memCursor{}, 10)
	err := drive(t, m, src, dst)
	var halt *HaltError
	require.ErrorAs(t, err, &halt)
	require.Equal(t, "not committed after resubmission", halt.Reason)
	require.Equal(t, 2, dst.forwards[hashOf(t, a)])
}

func TestResumesFromSavedCursor(t *testing.T) {
	a, b := manageEntity(1, "Create"), manageEntity(2, "Create")
	src := &fakeSource{chainID: srcChain, tip: 11, blocks: map[int64][][]byte{10: {raw(t, a)}, 11: {raw(t, b)}}}
	dst := newFakeTarget()
	cur := &memCursor{pos: Position{Height: 11}, saved: true}

	m := newMirror(t, src, dst, cur, 1) // start height ignored: a cursor is saved
	require.NoError(t, drive(t, m, src, dst))
	require.Equal(t, []string{hashOf(t, b)}, dst.order)
}

func TestRefusesTheWrongChains(t *testing.T) {
	cases := map[string]struct {
		src, dst string
	}{
		"source is the new chain": {dstChain, dstChain},
		"target is the old chain": {srcChain, srcChain},
		"unknown source":          {"audius-mainnet-gamma", dstChain},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dst := newFakeTarget()
			dst.chainID = tc.dst
			m := New(Config{SourceChainID: srcChain, TargetChainID: dstChain, StartHeight: 1},
				&fakeSource{chainID: tc.src}, dst, &memCursor{}, zap.NewNop())
			require.Error(t, m.Init(context.Background()))
		})
	}
}

func TestClassifyForwardErr(t *testing.T) {
	require.Equal(t, ErrRefused, classifyForwardErr(connect.NewError(connect.CodeInvalidArgument, errors.New("x"))))
	require.Equal(t, ErrFatal, classifyForwardErr(connect.NewError(connect.CodeFailedPrecondition, errors.New("chain"))))
	require.Nil(t, classifyForwardErr(errors.New("could not add tx to mempool mempool full")))
	require.Nil(t, classifyForwardErr(connect.NewError(connect.CodeUnavailable, errors.New("down"))))
}

func TestDryRunCountsAndChecksRoundTrip(t *testing.T) {
	a, p := manageEntity(1, "Create"), plays(1)
	src := &fakeSource{chainID: srcChain, tip: 11, blocks: map[int64][][]byte{
		10: {raw(t, a), raw(t, sla(1))},
		11: {raw(t, p)},
	}}
	dst := newFakeTarget()
	dst.committed[hashOf(t, a)] = 1

	r, err := DryRun(context.Background(), src, dst, srcChain, 10, 5)
	require.NoError(t, err)
	require.EqualValues(t, 11, r.To, "clamped to the source tip")
	require.EqualValues(t, 2, r.Blocks)
	require.EqualValues(t, 1, r.ManageEntity)
	require.EqualValues(t, 1, r.Plays)
	require.EqualValues(t, 1, r.NotMirrored)
	require.EqualValues(t, 1, r.OnTarget)
	require.Empty(t, r.HashMismatches)
	require.Empty(t, dst.forwards, "a dry run submits nothing")
}

// The operator's way past a halt: restart naming the refused transaction. The
// mirror passes over exactly that one, and keeps halting on anything else.
func TestOperatorSkipPassesOverOnlyTheNamedTx(t *testing.T) {
	good, bad, after, alsoBad := manageEntity(1, "Create"), manageEntity(2, "Create"), manageEntity(3, "Create"), manageEntity(4, "Create")
	src := &fakeSource{chainID: srcChain, tip: 12, blocks: map[int64][][]byte{
		10: {raw(t, good)},
		11: {raw(t, bad), raw(t, after)},
		12: {raw(t, alsoBad)},
	}}
	dst := newFakeTarget()
	dst.refuse = func(tx *corev1.SignedTransaction, _ map[string]int64) error {
		if id := tx.GetManageEntity().GetEntityId(); id == 2 || id == 4 {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("not authorized"))
		}
		return nil
	}
	cur := &memCursor{}

	m := newMirror(t, src, dst, cur, 10)
	err := drive(t, m, src, dst)
	var halt *HaltError
	require.ErrorAs(t, err, &halt)
	require.Equal(t, hashOf(t, bad), halt.Hash)
	require.Contains(t, halt.Hint(), "-skip-tx "+halt.Hash, "the hint names the exact flag to pass")

	// Restart naming it, in lower case as an operator might paste it.
	m2 := New(Config{
		SourceChainID: srcChain, TargetChainID: dstChain, StartHeight: 10,
		MaxInflight: 50, CommitTimeoutBlocks: 3, SkipTx: []string{strings.ToLower(halt.Hash)},
	}, src, dst, cur, zap.NewNop())
	m2.cfg.CursorSaveInterval = -1
	require.NoError(t, m2.Init(context.Background()))

	err = drive(t, m2, src, dst)
	var halt2 *HaltError
	require.ErrorAs(t, err, &halt2, "a refusal not named in -skip-tx still halts")
	require.Equal(t, hashOf(t, alsoBad), halt2.Hash)

	require.Equal(t, 1, dst.forwards[hashOf(t, bad)], "the skipped tx is not sent again")
	require.Equal(t, []string{hashOf(t, good), hashOf(t, after)}, dst.order)
	st := m2.Status()
	require.Equal(t, []SkippedTx{{Pos: Position{Height: 11, Index: 0}, Hash: hashOf(t, bad)}}, st.Skipped)
	require.Equal(t, Position{Height: 12, Index: 0}, cur.pos, "stopped at the next refusal")
}

func TestHaltWithoutATransactionHasNoSkipHint(t *testing.T) {
	require.Empty(t, (&HaltError{Pos: Position{Height: 5}, Reason: "source block from the wrong chain"}).Hint())
}
