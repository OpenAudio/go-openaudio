// Package mirror replays one chain's ManageEntity and Plays transactions onto
// another, in order, exactly once.
//
// It exists for the genesis rollover. The new chain is seeded from a snapshot
// of the old one, and everything the old chain accepts after that snapshot has
// to reach the new chain too, until every node has moved and the old chain
// stops receiving writes. Reading the old chain's blocks rather than the API's
// relay queue is what makes that complete: relay writes, the API's own write
// endpoints, and plays recorded by nodes still on the old chain all land in old
// blocks, and none of them pass through a single API code path.
//
// # Order
//
// Transactions are replayed in old-chain block order, and each block in its
// own transaction order. That order is what the old chain applied, so it is the
// only order guaranteed to be valid: a user's create precedes their update, a
// grant precedes the app acting under it.
//
// Core validates a ManageEntity against committed state at mempool admission,
// so a transaction that depends on one still in flight is refused. A refusal
// therefore does not halt immediately: the mirror waits for everything in
// flight to commit and retries once. Only a refusal with nothing in flight is
// final.
//
// # Block by block
//
// Each old block's transactions are all committed before the next old block is
// read, so no new block ever mixes transactions from two old blocks. That keeps
// the old chain's grouping, and the grouping matters to an indexer: it re-sorts
// a block's transactions before applying them, keys some rows on block number
// or block time (announcements, seen-notifications, price history), and checks
// some rules against block time. Packing several old blocks into one new block
// would make a from-genesis indexer of the new chain diverge from what the old
// chain's indexer produced.
//
// The reverse can still happen: an old block's transactions can be split across
// two new blocks if a proposal starts partway through submitting them. The
// mirror cannot prevent that, so it detects it and reports every split on
// /status.
//
// # Timestamps
//
// A live ManageEntity takes created_at and updated_at from the block time of
// the chain it lands on, so a mirrored write is stamped with when it reached
// the new chain, not when it happened on the old one. While the mirror works
// through a backlog those times are late; that is harmless because the API
// indexes those writes from the old chain. It matters from the indexer flip
// on, which is the reason to flip only once the mirror trails the old chain by
// a block or two. Plays carry their own timestamps and are unaffected.
//
// # Exactly once
//
// Plays carry no deduplication on chain, so a play sent twice counts twice. The
// mirror keeps a cursor -- the next old-chain position to process -- and only
// moves it past a transaction once the new chain has committed it. Before
// submitting anything it asks the new chain whether that hash is already
// committed, which covers the window between a commit and the cursor write.
//
// # Halting
//
// A refused write is never skipped automatically. It may matter, and continuing
// past it would replay later writes on top of a state the old chain never had.
// The mirror stops, reports why, and waits for an operator. Once they have
// looked at it, they can restart with that one transaction's hash in SkipTx;
// the mirror then passes over exactly that transaction, logs it, and counts it
// on /status. Nothing else is ever skipped.
//
// One refusal is answered rather than halted on, when an Attester is
// configured: a track whose audio someone else already holds a claim for, but
// whose own upload holds that same file. The new chain needs a node to attest
// the upload before the track; the mirror does that and retries. See attest.go.
package mirror

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// Position is a transaction's place on the source chain: its block, and its
// index among that block's transactions. The cursor is the next Position to
// process.
type Position struct {
	Height int64 `json:"height"`
	Index  int   `json:"index"`
}

func (p Position) String() string { return fmt.Sprintf("%d/%d", p.Height, p.Index) }

// Source is the old chain, read block by block in block order.
type Source interface {
	// ChainID is the chain the source serves.
	ChainID(ctx context.Context) (string, error)
	// Tip is the latest block the source can return.
	Tip(ctx context.Context) (int64, error)
	// Block returns the raw transactions of a block, in block order, and the
	// chain ID in its header.
	Block(ctx context.Context, height int64) (chainID string, txs [][]byte, err error)
}

// Target is the new chain.
type Target interface {
	// Status returns the target's chain ID and current height.
	Status(ctx context.Context) (chainID string, height int64, err error)
	// Forward submits a transaction to the mempool of every target node.
	Forward(ctx context.Context, tx *corev1.SignedTransaction) error
	// Committed reports whether a transaction hash is in a committed block,
	// and the height of that block.
	Committed(ctx context.Context, hash string) (height int64, ok bool, err error)
}

// CursorStore persists the cursor.
type CursorStore interface {
	// Load returns the saved cursor, and false when none has been saved.
	Load() (Position, bool, error)
	Save(Position) error
}

// Forward errors are sorted into three kinds, because each needs a different
// response. See classifyForwardErr.
var (
	// ErrRefused is a validation refusal: the target looked at the transaction
	// and said no. It may be a dependency on something still in flight.
	ErrRefused = errors.New("refused by target")
	// ErrFatal means retrying cannot help, e.g. the target is another chain.
	ErrFatal = errors.New("fatal target error")
)

// HaltError stops the mirror. It names the transaction and the reason.
type HaltError struct {
	Pos    Position
	Hash   string
	Reason string
	Err    error
}

func (e *HaltError) Error() string {
	return fmt.Sprintf("mirror halted at %s (tx %s): %s: %v", e.Pos, e.Hash, e.Reason, e.Err)
}

// Hint tells the operator how to continue past this halt, when skipping is an
// option at all: a halt that names no transaction (wrong chain, say) is not.
func (e *HaltError) Hint() string {
	if e.Hash == "" {
		return ""
	}
	return fmt.Sprintf("investigate, then restart with -skip-tx %s to pass over this one transaction", e.Hash)
}

func (e *HaltError) Unwrap() error { return e.Err }

type Config struct {
	SourceChainID string
	TargetChainID string
	// StartHeight is where a first run begins, when no cursor is saved.
	StartHeight int64
	// MaxInflight bounds transactions submitted but not yet committed.
	MaxInflight int
	// CommitTimeoutBlocks is how long a submitted transaction may stay
	// uncommitted before it is resubmitted once. Core expires a forwarded
	// transaction from its mempool 10 blocks after admission, so this must be
	// longer than that or a resubmit could race the original.
	CommitTimeoutBlocks int64
	// StartupWaitBlocks is how many target blocks to wait before the first
	// submission. A previous run's in-flight transactions have either
	// committed or expired by then, so the committed check is reliable and
	// nothing is submitted twice.
	StartupWaitBlocks int64
	// SkipTx names transactions, by hash, that an operator has looked at and
	// decided not to mirror. Each is passed over when the mirror reaches it.
	SkipTx []string
	// Attester, when set, answers content-auth refusals by attesting the
	// track's own upload. Nil leaves every refusal a halt.
	Attester     *Attester
	PollInterval time.Duration
	// CursorSaveInterval throttles cursor writes while the mirror is busy.
	CursorSaveInterval time.Duration
}

func (c *Config) setDefaults() {
	if c.MaxInflight <= 0 {
		c.MaxInflight = 500
	}
	if c.CommitTimeoutBlocks <= 0 {
		c.CommitTimeoutBlocks = 12
	}
	if c.PollInterval <= 0 {
		c.PollInterval = time.Second
	}
	if c.CursorSaveInterval <= 0 {
		c.CursorSaveInterval = time.Second
	}
}

// entry is one source transaction between "read" and "settled".
type entry struct {
	pos  Position
	hash string
	tx   *corev1.SignedTransaction
	// done means nothing is left to wait for: not a mirrored type, already
	// committed, or committed since submission.
	done        bool
	submittedAt int64
	resubmitted bool
	// attestation is what the mirror submitted to answer a content-auth
	// refusal of this transaction, and attestedAt the target height it was
	// submitted at. attested means it committed, so a further refusal is
	// final.
	attestation     *corev1.SignedTransaction
	attestationHash string
	attestedAt      int64
	attested        bool
}

type Mirror struct {
	cfg    Config
	source Source
	target Target
	cursor CursorStore
	logger *zap.Logger

	// next is the next source position to read. Everything before it is
	// either settled or in inflight.
	next Position
	// settled is the cursor: everything before it is committed or skipped.
	settled   Position
	savedAt   time.Time
	savedPos  Position
	inflight  []*entry
	sourceTip int64
	// targetHeight is read once per Step and used for submission and
	// timeout bookkeeping, rather than asking the target on every tx.
	targetHeight int64

	blockHeight int64
	blockTxs    [][]byte

	// splitTrack is the source block whose commits are being recorded, and
	// splitHeights the target heights they landed at. More than one height
	// means the block was split across target blocks.
	splitTrack   int64
	splitHeights map[int64]struct{}

	skip map[string]struct{}

	// draining holds back new submissions after a refusal until everything in
	// flight has committed; retry is the refused transaction.
	draining bool
	retry    *entry

	statusMu sync.Mutex
	status   Status
}

// Status is what the mirror reports about itself.
type Status struct {
	SourceChainID   string   `json:"source_chain_id"`
	TargetChainID   string   `json:"target_chain_id"`
	SourceTip       int64    `json:"source_tip"`
	Cursor          Position `json:"cursor"`
	LagBlocks       int64    `json:"lag_blocks"`
	Inflight        int      `json:"inflight"`
	Draining        bool     `json:"draining"`
	Submitted       int64    `json:"submitted"`
	Committed       int64    `json:"committed"`
	AlreadyOnTarget int64    `json:"already_on_target"`
	// SplitBlocks counts source blocks whose transactions landed in more than
	// one target block; LastSplit is the most recent.
	SplitBlocks int64      `json:"split_blocks"`
	LastSplit   *SplitInfo `json:"last_split,omitempty"`
	// Skipped lists the operator-skipped transactions the mirror has passed
	// over in this run.
	Skipped []SkippedTx `json:"skipped,omitempty"`
	// Attested lists the attestations the mirror made in this run.
	Attested   []AttestedTx `json:"attested,omitempty"`
	Halted     bool         `json:"halted"`
	HaltReason string       `json:"halt_reason,omitempty"`
	HaltHint   string       `json:"halt_hint,omitempty"`
	LastError  string       `json:"last_error,omitempty"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

// SkippedTx is a transaction passed over because an operator named it.
type SkippedTx struct {
	Pos  Position `json:"pos"`
	Hash string   `json:"hash"`
}

// AttestedTx is a transaction the mirror attested a track's upload for.
type AttestedTx struct {
	Pos             Position `json:"pos"`
	Hash            string   `json:"hash"`
	AttestationHash string   `json:"attestation_hash"`
	UserID          int64    `json:"user_id"`
	Cids            []string `json:"cids"`
}

// SplitInfo names a source block that landed in more than one target block.
type SplitInfo struct {
	SourceHeight  int64   `json:"source_height"`
	TargetHeights []int64 `json:"target_heights"`
}

func New(cfg Config, source Source, target Target, cursor CursorStore, logger *zap.Logger) *Mirror {
	cfg.setDefaults()
	skip := make(map[string]struct{}, len(cfg.SkipTx))
	for _, h := range cfg.SkipTx {
		skip[strings.ToUpper(strings.TrimSpace(h))] = struct{}{}
	}
	return &Mirror{
		skip:   skip,
		cfg:    cfg,
		source: source,
		target: target,
		cursor: cursor,
		logger: logger.With(zap.String("service", "mirror")),
		status: Status{SourceChainID: cfg.SourceChainID, TargetChainID: cfg.TargetChainID},
	}
}

// Status returns a snapshot of the mirror's state.
func (m *Mirror) Status() Status {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	return m.status
}

func (m *Mirror) updateStatus(f func(*Status)) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	f(&m.status)
	m.status.Cursor = m.settled
	m.status.Inflight = len(m.inflight)
	m.status.Draining = m.draining
	m.status.SourceTip = m.sourceTip
	if m.sourceTip > 0 {
		m.status.LagBlocks = max(m.sourceTip-m.settled.Height+1, 0)
	}
	m.status.UpdatedAt = time.Now()
}

// Init checks both chains are the ones configured and loads the cursor. It
// refuses to start against the wrong chain, which is the one mistake that
// would quietly write the wrong history somewhere.
func (m *Mirror) Init(ctx context.Context) error {
	srcChain, err := m.source.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("source chain id: %w", err)
	}
	if srcChain != m.cfg.SourceChainID {
		return fmt.Errorf("source is chain %q, expected %q", srcChain, m.cfg.SourceChainID)
	}
	dstChain, _, err := m.target.Status(ctx)
	if err != nil {
		return fmt.Errorf("target status: %w", err)
	}
	if dstChain != m.cfg.TargetChainID {
		return fmt.Errorf("target is chain %q, expected %q", dstChain, m.cfg.TargetChainID)
	}
	if srcChain == dstChain {
		return fmt.Errorf("source and target are the same chain %q", srcChain)
	}

	pos, ok, err := m.cursor.Load()
	if err != nil {
		return fmt.Errorf("load cursor: %w", err)
	}
	if !ok {
		if m.cfg.StartHeight <= 0 {
			return errors.New("no saved cursor and no start height: pass the first source block to mirror")
		}
		pos = Position{Height: m.cfg.StartHeight}
		m.logger.Info("no saved cursor, starting fresh", zap.Stringer("start", pos))
	} else {
		m.logger.Info("resuming from saved cursor", zap.Stringer("cursor", pos))
	}
	m.next, m.settled, m.savedPos = pos, pos, pos
	m.updateStatus(func(*Status) {})
	return nil
}

// Run mirrors until ctx ends or the mirror halts. A halt returns a *HaltError.
func (m *Mirror) Run(ctx context.Context) error {
	if err := m.waitStartup(ctx); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			m.saveCursor(true)
			return ctx.Err()
		}
		progressed, err := m.Step(ctx)
		if err != nil {
			var halt *HaltError
			if errors.As(err, &halt) {
				m.saveCursor(true)
				m.logger.Error("mirror halted", zap.Stringer("pos", halt.Pos), zap.String("tx", halt.Hash),
					zap.String("reason", halt.Reason), zap.Error(halt.Err), zap.String("next_step", halt.Hint()))
				m.updateStatus(func(s *Status) {
					s.Halted, s.HaltReason, s.HaltHint = true, halt.Error(), halt.Hint()
				})
				return err
			}
			m.logger.Warn("mirror step failed, retrying", zap.Error(err))
			m.updateStatus(func(s *Status) { s.LastError = err.Error() })
			progressed = false
		}
		if !progressed {
			sleep(ctx, m.cfg.PollInterval)
		}
	}
}

func (m *Mirror) waitStartup(ctx context.Context) error {
	if m.cfg.StartupWaitBlocks <= 0 {
		return nil
	}
	start := int64(-1)
	for {
		_, h, err := m.target.Status(ctx)
		switch {
		case err != nil:
			m.logger.Warn("target status failed, retrying", zap.Error(err))
		case start < 0:
			start = h
			m.logger.Info("waiting for a previous run's transactions to commit or expire",
				zap.Int64("target_height", start), zap.Int64("blocks", m.cfg.StartupWaitBlocks))
		case h >= start+m.cfg.StartupWaitBlocks:
			return nil
		}
		sleep(ctx, m.cfg.PollInterval)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// Step does one round of work: settle what has committed, then submit more. It
// reports whether anything moved, so the caller knows whether to wait.
func (m *Mirror) Step(ctx context.Context) (bool, error) {
	defer m.updateStatus(func(*Status) {})
	_, h, err := m.target.Status(ctx)
	if err != nil {
		return false, fmt.Errorf("target status: %w", err)
	}
	m.targetHeight = h
	settled, err := m.settle(ctx)
	if err != nil {
		m.saveCursor(false)
		return settled, err
	}
	filled, err := m.fill(ctx)
	m.saveCursor(false)
	return settled || filled, err
}

// settle pops committed transactions off the front of inflight and moves the
// cursor past them. It stops at the first one still pending, so the cursor
// only ever covers a contiguous committed prefix.
func (m *Mirror) settle(ctx context.Context) (bool, error) {
	progressed := false
	for len(m.inflight) > 0 {
		e := m.inflight[0]
		if !e.done {
			h, ok, err := m.target.Committed(ctx, e.hash)
			if err != nil {
				return progressed, fmt.Errorf("check %s: %w", e.hash, err)
			}
			if ok {
				e.done = true
				m.noteCommit(e.pos.Height, h)
				m.updateStatus(func(s *Status) { s.Committed++ })
			} else {
				return progressed, m.checkTimeout(ctx, e)
			}
		}
		m.inflight = m.inflight[1:]
		m.settled = nextPosition(e.pos)
		progressed = true
	}

	// Nothing in flight: a refused transaction can be retried against the
	// state everything before it produced.
	if m.draining && len(m.inflight) == 0 {
		e := m.retry
		if e.attestation != nil && !e.attested {
			ok, err := m.awaitAttestation(ctx, e)
			if err != nil || !ok {
				return progressed, err
			}
		}
		m.draining, m.retry = false, nil
		// Everything before the refused tx is settled; it is the next thing
		// the cursor waits on, and where a halt leaves it.
		m.settled = e.pos
		m.logger.Info("in-flight transactions committed, retrying refused tx", zap.Stringer("pos", e.pos), zap.String("tx", e.hash))
		if err := m.submit(ctx, e); err != nil {
			var halt *HaltError
			if !errors.As(err, &halt) {
				// Transient: park it again rather than lose it.
				m.draining, m.retry = true, e
			}
			return progressed, err
		}
		progressed = true
	}

	// Nothing outstanding: everything read so far is settled, including any
	// empty blocks read past since the last transaction.
	if len(m.inflight) == 0 && !m.draining {
		m.settled = m.next
	}
	return progressed, nil
}

// checkTimeout resubmits a transaction that has sat uncommitted past the
// timeout, once, and halts if it still does not commit. A transaction can be
// admitted and then dropped by a proposer that rejects it, which the mempool
// never reports, so a missing commit is the only signal.
func (m *Mirror) checkTimeout(ctx context.Context, e *entry) error {
	h := m.targetHeight
	if h <= e.submittedAt+m.cfg.CommitTimeoutBlocks {
		return nil
	}
	if e.resubmitted {
		return &HaltError{Pos: e.pos, Hash: e.hash, Reason: "not committed after resubmission",
			Err: fmt.Errorf("submitted at target height %d, now %d", e.submittedAt, h)}
	}
	m.logger.Warn("tx not committed in time, resubmitting once",
		zap.Stringer("pos", e.pos), zap.String("tx", e.hash), zap.Int64("submitted_at", e.submittedAt), zap.Int64("height", h))
	if err := m.target.Forward(ctx, e.tx); err != nil {
		if kind := classifyForwardErr(err); kind == ErrRefused || kind == ErrFatal {
			return &HaltError{Pos: e.pos, Hash: e.hash, Reason: "refused on resubmission", Err: err}
		}
		return fmt.Errorf("resubmit %s: %w", e.hash, err)
	}
	e.resubmitted, e.submittedAt = true, h
	return nil
}

// fill reads source transactions and submits them until inflight is full, the
// source is exhausted, or a refusal starts a drain.
func (m *Mirror) fill(ctx context.Context) (bool, error) {
	progressed := false
	for !m.draining && len(m.inflight) < m.cfg.MaxInflight {
		ok, err := m.fillOne(ctx)
		if err != nil || !ok {
			return progressed, err
		}
		progressed = true
	}
	return progressed, nil
}

func (m *Mirror) fillOne(ctx context.Context) (bool, error) {
	pos := m.next
	if m.blockTxs == nil || m.blockHeight != pos.Height {
		if pos.Height > m.sourceTip {
			tip, err := m.source.Tip(ctx)
			if err != nil {
				return false, fmt.Errorf("source tip: %w", err)
			}
			m.sourceTip = tip
			if pos.Height > tip {
				return false, nil // caught up
			}
		}
		chainID, txs, err := m.source.Block(ctx, pos.Height)
		if err != nil {
			return false, fmt.Errorf("source block %d: %w", pos.Height, err)
		}
		if chainID != m.cfg.SourceChainID {
			return false, &HaltError{Pos: pos, Reason: "source block from the wrong chain",
				Err: fmt.Errorf("block %d is chain %q, expected %q", pos.Height, chainID, m.cfg.SourceChainID)}
		}
		if txs == nil {
			txs = [][]byte{}
		}
		m.blockHeight, m.blockTxs = pos.Height, txs
	}

	if pos.Index >= len(m.blockTxs) {
		// End of the block. The next block is not read until everything from
		// this one has committed, so no target block mixes two source blocks.
		if m.pendingCount() > 0 {
			return false, nil
		}
		m.finishSplitCheck()
		m.next = Position{Height: pos.Height + 1}
		if len(m.inflight) == 0 {
			m.settled = m.next
		}
		return true, nil
	}

	e := &entry{pos: pos}
	m.next = nextPosition(pos)
	tx, mirrored := decodeMirrored(m.blockTxs[pos.Index])
	if !mirrored {
		e.done = true
		m.inflight = append(m.inflight, e)
		return true, nil
	}
	e.tx = tx
	// Hash what the target will hash: core re-marshals the message it is
	// handed, so this is the key it commits under.
	b, err := proto.Marshal(tx)
	if err != nil {
		return false, &HaltError{Pos: pos, Reason: "cannot marshal source tx", Err: err}
	}
	e.hash = common.ToTxHashFromBytes(b)

	if _, skip := m.skip[e.hash]; skip {
		m.logger.Warn("skipping tx by operator request", zap.Stringer("pos", pos), zap.String("tx", e.hash),
			zap.String("type", txKind(tx)))
		e.done = true
		m.inflight = append(m.inflight, e)
		m.updateStatus(func(s *Status) { s.Skipped = append(s.Skipped, SkippedTx{Pos: pos, Hash: e.hash}) })
		return true, nil
	}

	h, ok, err := m.target.Committed(ctx, e.hash)
	if err != nil {
		m.next = pos
		return false, fmt.Errorf("check %s: %w", e.hash, err)
	}
	if ok {
		e.done = true
		m.noteCommit(pos.Height, h)
		m.inflight = append(m.inflight, e)
		m.updateStatus(func(s *Status) { s.AlreadyOnTarget++ })
		return true, nil
	}

	if err := m.submit(ctx, e); err != nil {
		var halt *HaltError
		if !errors.As(err, &halt) && !m.draining {
			m.next = pos // transient: read it again next round
		}
		return false, err
	}
	return true, nil
}

// submit forwards e and queues it. A refusal with transactions in flight
// starts a drain and parks e for retry; one with nothing in flight halts.
func (m *Mirror) submit(ctx context.Context, e *entry) error {
	err := m.target.Forward(ctx, e.tx)
	if err == nil {
		e.submittedAt = m.targetHeight
		m.inflight = append(m.inflight, e)
		m.updateStatus(func(s *Status) { s.Submitted++ })
		return nil
	}
	switch classifyForwardErr(err) {
	case ErrFatal:
		return &HaltError{Pos: e.pos, Hash: e.hash, Reason: "target rejected the forward", Err: err}
	case ErrRefused:
		if m.pendingCount() > 0 {
			m.logger.Info("tx refused with transactions in flight, draining before retry",
				zap.Stringer("pos", e.pos), zap.String("tx", e.hash), zap.Error(err))
			m.draining, m.retry = true, e
			return nil
		}
		if m.cfg.Attester != nil && !e.attested && isUnclaimedCidRefusal(err) {
			return m.startAttestation(ctx, e, err)
		}
		return &HaltError{Pos: e.pos, Hash: e.hash, Reason: "refused by target", Err: err}
	default:
		return fmt.Errorf("forward %s: %w", e.hash, err)
	}
}

// startAttestation answers a content-auth refusal of e: it submits an
// attestation for e's own upload and parks e, the way a dependency refusal
// is parked, until the attestation commits. settle then retries e once.
func (m *Mirror) startAttestation(ctx context.Context, e *entry, refusal error) error {
	att, err := m.cfg.Attester.attestationFor(ctx, e.tx)
	var na errNotAttestable
	if errors.As(err, &na) {
		return &HaltError{Pos: e.pos, Hash: e.hash, Reason: "refused by target, " + na.Error(), Err: refusal}
	}
	if err != nil {
		return fmt.Errorf("attest %s: %w", e.hash, err)
	}
	attHash, err := txHash(att)
	if err != nil {
		return &HaltError{Pos: e.pos, Hash: e.hash, Reason: "cannot marshal attestation", Err: err}
	}
	e.attestation, e.attestationHash = att, attHash

	// A previous run may have got this far; the signature is deterministic,
	// so its attestation has this same hash.
	_, ok, err := m.target.Committed(ctx, attHash)
	if err != nil {
		return fmt.Errorf("check attestation %s: %w", attHash, err)
	}
	if !ok {
		if err := m.target.Forward(ctx, att); err != nil {
			if kind := classifyForwardErr(err); kind == ErrRefused || kind == ErrFatal {
				return &HaltError{Pos: e.pos, Hash: e.hash, Reason: "attestation refused by target", Err: err}
			}
			return fmt.Errorf("forward attestation %s: %w", attHash, err)
		}
		m.logger.Info("track's audio claimed by another user, attesting its own upload",
			zap.Stringer("pos", e.pos), zap.String("tx", e.hash), zap.String("attestation", attHash),
			zap.Int64("user_id", att.GetContentAttestation().GetUserId()), zap.Strings("cids", att.GetContentAttestation().GetCids()))
	}
	e.attestedAt = m.targetHeight
	m.draining, m.retry = true, e
	return nil
}

// awaitAttestation reports whether the attestation parked e is waiting on has
// committed, and halts if it does not commit in time.
func (m *Mirror) awaitAttestation(ctx context.Context, e *entry) (bool, error) {
	_, ok, err := m.target.Committed(ctx, e.attestationHash)
	if err != nil {
		return false, fmt.Errorf("check attestation %s: %w", e.attestationHash, err)
	}
	if !ok {
		if m.targetHeight > e.attestedAt+m.cfg.CommitTimeoutBlocks {
			return false, &HaltError{Pos: e.pos, Hash: e.hash, Reason: "attestation not committed",
				Err: fmt.Errorf("attestation %s submitted at target height %d, now %d", e.attestationHash, e.attestedAt, m.targetHeight)}
		}
		return false, nil
	}
	e.attested = true
	ca := e.attestation.GetContentAttestation()
	m.updateStatus(func(s *Status) {
		s.Attested = append(s.Attested, AttestedTx{Pos: e.pos, Hash: e.hash, AttestationHash: e.attestationHash,
			UserID: ca.GetUserId(), Cids: ca.GetCids()})
	})
	return true, nil
}

// noteCommit records the target height a source block's transaction landed at.
func (m *Mirror) noteCommit(sourceHeight, targetHeight int64) {
	if sourceHeight != m.splitTrack {
		m.finishSplitCheck()
		m.splitTrack, m.splitHeights = sourceHeight, map[int64]struct{}{}
	}
	m.splitHeights[targetHeight] = struct{}{}
}

// finishSplitCheck reports the tracked source block if its transactions landed
// in more than one target block. It runs once the block is fully committed.
func (m *Mirror) finishSplitCheck() {
	if len(m.splitHeights) > 1 {
		heights := make([]int64, 0, len(m.splitHeights))
		for h := range m.splitHeights {
			heights = append(heights, h)
		}
		slices.Sort(heights)
		split := &SplitInfo{SourceHeight: m.splitTrack, TargetHeights: heights}
		m.logger.Warn("source block split across target blocks",
			zap.Int64("source_height", split.SourceHeight), zap.Int64s("target_heights", heights))
		m.updateStatus(func(s *Status) { s.SplitBlocks++; s.LastSplit = split })
	}
	m.splitTrack, m.splitHeights = 0, nil
}

func (m *Mirror) pendingCount() int {
	n := 0
	for _, e := range m.inflight {
		if !e.done {
			n++
		}
	}
	return n
}

func (m *Mirror) saveCursor(force bool) {
	if m.settled == m.savedPos {
		return
	}
	if !force && time.Since(m.savedAt) < m.cfg.CursorSaveInterval {
		return
	}
	if err := m.cursor.Save(m.settled); err != nil {
		// The next save retries. A lost cursor write costs only re-checking
		// hashes the target already has.
		m.logger.Error("save cursor failed", zap.Error(err))
		return
	}
	m.savedPos, m.savedAt = m.settled, time.Now()
}

func nextPosition(p Position) Position { return Position{Height: p.Height, Index: p.Index + 1} }

// DryRunReport is what a read-only scan of source blocks found.
type DryRunReport struct {
	From, To int64
	Blocks   int64
	// BlocksWithMirrored is how many blocks hold at least one transaction
	// the mirror would send: each costs about one target block when
	// following block by block, and empty blocks cost nothing.
	BlocksWithMirrored int64
	Transactions       int64
	ManageEntity       int64
	Plays              int64
	NotMirrored        int64
	OnTarget           int64
	HashMismatches     []string
}

// DryRun reads source blocks [from, from+n) and reports what the mirror would
// do, without submitting anything. It also checks the one assumption the
// exactly-once guarantee rests on: that a decoded transaction re-marshals to
// the bytes the source committed, so it keeps its hash on the target. A
// mismatch means the target would commit it under a different hash and the
// "already on target" check could not see it.
func DryRun(ctx context.Context, source Source, target Target, sourceChainID string, from, n int64) (*DryRunReport, error) {
	r := &DryRunReport{From: from, To: from + n - 1}
	tip, err := source.Tip(ctx)
	if err != nil {
		return nil, fmt.Errorf("source tip: %w", err)
	}
	if r.To > tip {
		r.To = tip
	}
	for h := from; h <= r.To; h++ {
		chainID, txs, err := source.Block(ctx, h)
		if err != nil {
			return r, fmt.Errorf("source block %d: %w", h, err)
		}
		if chainID != sourceChainID {
			return r, fmt.Errorf("block %d is chain %q, expected %q", h, chainID, sourceChainID)
		}
		r.Blocks++
		counted := false
		for _, b := range txs {
			r.Transactions++
			tx, ok := decodeMirrored(b)
			if !ok {
				r.NotMirrored++
				continue
			}
			if !counted {
				r.BlocksWithMirrored++
				counted = true
			}
			if tx.GetPlays() != nil {
				r.Plays++
			} else {
				r.ManageEntity++
			}
			re, err := proto.Marshal(tx)
			if err != nil {
				return r, fmt.Errorf("block %d: marshal: %w", h, err)
			}
			hash := common.ToTxHashFromBytes(re)
			if src := common.ToTxHashFromBytes(b); src != hash {
				r.HashMismatches = append(r.HashMismatches, fmt.Sprintf("block %d: source %s re-marshals to %s", h, src, hash))
			}
			if target != nil {
				_, on, err := target.Committed(ctx, hash)
				if err != nil {
					return r, fmt.Errorf("check %s: %w", hash, err)
				}
				if on {
					r.OnTarget++
				}
			}
		}
	}
	return r, nil
}

// decodeMirrored returns the transaction if it is one the mirror replays:
// ManageEntity and Plays. Everything else on the old chain is either per-node
// (registrations, storage proofs, SLA rollups, attestations, mediorum
// operations) or not needed on the new chain.
func decodeMirrored(raw []byte) (*corev1.SignedTransaction, bool) {
	var tx corev1.SignedTransaction
	if err := proto.Unmarshal(raw, &tx); err != nil {
		return nil, false // v2 transactions and anything else unrecognized
	}
	switch tx.Transaction.(type) {
	case *corev1.SignedTransaction_ManageEntity, *corev1.SignedTransaction_Plays:
		return &tx, true
	default:
		return nil, false
	}
}

func txKind(tx *corev1.SignedTransaction) string {
	if me := tx.GetManageEntity(); me != nil {
		return me.GetEntityType() + " " + me.GetAction()
	}
	return "Plays"
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
