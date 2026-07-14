package pages

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ConsensusClassification is a coarse, operator-facing label for the state of
// the CometBFT consensus engine. It is derived entirely from the sanitized
// `/consensus_state` round data (no peer identities / topology), so it is safe
// to surface on the public console.
type ConsensusClassification string

const (
	// ConsensusHealthy: blocks are committing and rounds are low.
	ConsensusHealthy ConsensusClassification = "healthy"
	// ConsensusCatchingUp: node is state/block syncing; halt detection is suppressed.
	ConsensusCatchingUp ConsensusClassification = "catching_up"
	// ConsensusDegraded: rounds are elevated but the chain still looks like it's
	// making (slow) progress. An early warning, not a halt.
	ConsensusDegraded ConsensusClassification = "degraded"
	// ConsensusHaltLiveness: stuck, and votes are overwhelmingly nil — nobody can
	// build/accept a proposal. This is the "every proposal rejected" liveness
	// failure (e.g. oversized blocks, ProcessProposal rejecting every batch).
	ConsensusHaltLiveness ConsensusClassification = "halted_liveness"
	// ConsensusHaltSplit: stuck, and within a round votes are split across two or
	// more competing block hashes — a sign of non-determinism / app-hash divergence.
	ConsensusHaltSplit ConsensusClassification = "halted_split"
	// ConsensusHaltUnknown: stuck, but the vote pattern doesn't cleanly match
	// either bucket above.
	ConsensusHaltUnknown ConsensusClassification = "halted_unknown"
	// ConsensusUnknown: consensus state couldn't be read/parsed.
	ConsensusUnknown ConsensusClassification = "unknown"
)

// IsHalt reports whether the classification represents a halted chain.
func (c ConsensusClassification) IsHalt() bool {
	return strings.HasPrefix(string(c), "halted")
}

// ShowPanel reports whether the consensus panel is worth rendering. It's only
// shown when the chain needs attention — halted, or degraded (elevated rounds,
// the early-warning before a halt). Healthy / catching-up / unknown add no
// signal beyond the overview's existing status row, so the panel is hidden.
func (h *ConsensusHealth) ShowPanel() bool {
	if h == nil {
		return false
	}
	return h.Halted || h.Classification == ConsensusDegraded
}

// HaltThresholds tunes when the analyzer decides the chain is halted vs merely
// degraded. Zero values disable the corresponding check.
type HaltThresholds struct {
	// HaltAfter: if no block has committed for at least this long, treat as halted.
	HaltAfter time.Duration
	// HighRound: if the current consensus round is >= this, treat as halted
	// regardless of elapsed time (rounds are normally 0).
	HighRound int32
	// WarnRound: round >= this (but below HighRound / time threshold) is "degraded".
	WarnRound int32
}

// DefaultHaltThresholds are conservative defaults chosen to avoid false
// positives during normal (single-digit-second) block cadence.
func DefaultHaltThresholds() HaltThresholds {
	return HaltThresholds{
		HaltAfter: 60 * time.Second,
		HighRound: 10,
		WarnRound: 3,
	}
}

// ConsensusHealth is the sanitized view model rendered by the console. It
// deliberately omits peer IDs and per-validator identities that a full
// dump_consensus_state would expose.
type ConsensusHealth struct {
	Classification ConsensusClassification
	Halted         bool

	Height int64
	Round  int32
	Step   string

	SecondsSinceLastBlock float64
	LastBlockTime         time.Time
	CatchingUp            bool

	// Participation, taken from the height's most-populated round.
	TotalValidators    int
	VotingValidators   int
	PrevotePowerFrac   float64 // 0..1 of total voting power that prevoted
	PrecommitPowerFrac float64 // 0..1 of total voting power that precommitted

	// Vote breakdown aggregated across every round of the current height.
	NilPrevotes         int // present votes cast for nil
	BlockPrevotes       int // present votes cast for a real block
	DistinctBlockHashes int // distinct non-nil block hashes seen across all rounds
	MaxHashesInRound    int // most distinct non-nil hashes seen within a single round

	Summary string
}

// --- raw shapes for the /consensus_state round_state payload ---

type rawRoundState struct {
	HRS           string       `json:"height/round/step"`
	StartTime     time.Time    `json:"start_time"`
	HeightVoteSet []rawVoteSet `json:"height_vote_set"`
}

type rawVoteSet struct {
	Round              int      `json:"round"`
	Prevotes           []string `json:"prevotes"`
	PrevotesBitArray   string   `json:"prevotes_bit_array"`
	Precommits         []string `json:"precommits"`
	PrecommitsBitArray string   `json:"precommits_bit_array"`
}

// AnalyzeConsensusState turns a raw CometBFT round_state payload (the
// `round_state` field of a /consensus_state response) plus basic status info
// into a sanitized ConsensusHealth summary.
//
// latestBlockTime / catchingUp come from the node's /status. now is injected so
// the function is deterministic and testable.
func AnalyzeConsensusState(roundStateJSON []byte, latestBlockTime time.Time, catchingUp bool, now time.Time, th HaltThresholds) (*ConsensusHealth, error) {
	var rs rawRoundState
	if err := json.Unmarshal(roundStateJSON, &rs); err != nil {
		return nil, fmt.Errorf("parse round state: %w", err)
	}

	h := &ConsensusHealth{
		CatchingUp:    catchingUp,
		LastBlockTime: latestBlockTime,
	}
	h.Height, h.Round, h.Step = parseHRS(rs.HRS)
	if !latestBlockTime.IsZero() {
		h.SecondsSinceLastBlock = now.Sub(latestBlockTime).Seconds()
	}

	distinctAcross := map[string]struct{}{}
	bestRoundIdx, bestVoting := -1, -1

	for i, vs := range rs.HeightVoteSet {
		voting := 0
		perRound := map[string]struct{}{}
		for _, v := range vs.Prevotes {
			present, blockID := voteBlockID(v)
			if !present {
				continue
			}
			voting++
			if blockID == "" {
				h.NilPrevotes++
			} else {
				h.BlockPrevotes++
				distinctAcross[blockID] = struct{}{}
				perRound[blockID] = struct{}{}
			}
		}
		if len(perRound) > h.MaxHashesInRound {
			h.MaxHashesInRound = len(perRound)
		}
		if voting > bestVoting {
			bestVoting = voting
			bestRoundIdx = i
		}
	}
	h.DistinctBlockHashes = len(distinctAcross)

	if bestRoundIdx >= 0 {
		vs := rs.HeightVoteSet[bestRoundIdx]
		h.TotalValidators = len(vs.Prevotes)
		h.VotingValidators = bestVoting
		h.PrevotePowerFrac = powerFrac(vs.PrevotesBitArray)
		h.PrecommitPowerFrac = powerFrac(vs.PrecommitsBitArray)
	}

	h.Classification = classifyConsensus(h, th)
	h.Halted = h.Classification.IsHalt()
	h.Summary = summarizeConsensus(h)
	return h, nil
}

func classifyConsensus(h *ConsensusHealth, th HaltThresholds) ConsensusClassification {
	if h.CatchingUp {
		return ConsensusCatchingUp
	}

	haltedByTime := th.HaltAfter > 0 && !h.LastBlockTime.IsZero() &&
		h.SecondsSinceLastBlock >= th.HaltAfter.Seconds()
	haltedByRound := th.HighRound > 0 && h.Round >= th.HighRound

	if !haltedByTime && !haltedByRound {
		if th.WarnRound > 0 && h.Round >= th.WarnRound {
			return ConsensusDegraded
		}
		return ConsensusHealthy
	}

	// Halted: classify the failure mode.
	// Two or more competing block hashes inside a single round points at
	// non-determinism / an app-hash split rather than a plain liveness stall.
	if h.MaxHashesInRound >= 2 {
		return ConsensusHaltSplit
	}
	// Overwhelmingly nil prevotes => nobody can agree on a proposal.
	if h.NilPrevotes > 0 && h.NilPrevotes >= h.BlockPrevotes {
		return ConsensusHaltLiveness
	}
	return ConsensusHaltUnknown
}

func summarizeConsensus(h *ConsensusHealth) string {
	switch h.Classification {
	case ConsensusHealthy:
		return "Consensus healthy — committing blocks."
	case ConsensusCatchingUp:
		return "Node is syncing; consensus health checks are paused until caught up."
	case ConsensusDegraded:
		return fmt.Sprintf("Elevated round (%d) at height %d — consensus is slow but progressing.", h.Round, h.Height)
	case ConsensusHaltLiveness:
		return fmt.Sprintf("HALT: height %d stuck at round %d with %d nil prevotes — no proposal is being accepted (liveness failure).", h.Height, h.Round, h.NilPrevotes)
	case ConsensusHaltSplit:
		return fmt.Sprintf("HALT: height %d stuck at round %d with %d competing block hashes in a round — possible non-determinism / app-hash split.", h.Height, h.Round, h.MaxHashesInRound)
	case ConsensusHaltUnknown:
		return fmt.Sprintf("HALT: height %d stuck at round %d — cause unclear from vote data.", h.Height, h.Round)
	default:
		return "Consensus state unavailable."
	}
}

// UnknownConsensusHealth is returned when the RPC/consensus state can't be read.
func UnknownConsensusHealth() *ConsensusHealth {
	return &ConsensusHealth{
		Classification: ConsensusUnknown,
		Summary:        summarizeConsensus(&ConsensusHealth{Classification: ConsensusUnknown}),
	}
}

// --- parsing helpers ---

func parseHRS(hrs string) (height int64, round int32, step string) {
	parts := strings.Split(hrs, "/")
	if len(parts) != 3 {
		return 0, 0, "unknown"
	}
	fmt.Sscan(parts[0], &height)
	var r int64
	fmt.Sscan(parts[1], &r)
	round = int32(r)
	var s int
	fmt.Sscan(parts[2], &s)
	return height, round, stepName(s)
}

func stepName(step int) string {
	switch step {
	case 1:
		return "NewHeight"
	case 2:
		return "NewRound"
	case 3:
		return "Propose"
	case 4:
		return "Prevote"
	case 5:
		return "PrevoteWait"
	case 6:
		return "Precommit"
	case 7:
		return "PrecommitWait"
	case 8:
		return "Commit"
	default:
		return fmt.Sprintf("step %d", step)
	}
}

// voteBlockID extracts the block a vote was cast for from a CometBFT vote string:
//
//	Vote{7:326D405ABA6E 28173293/00/SIGNED_MSG_TYPE_PREVOTE(Prevote) <blockhash> <partset> <sig> @ <time>}
//	nil-Vote
//
// present is false when the validator has cast no vote (nil-Vote). When present,
// blockID is "" for a vote for nil, or the block-hash fingerprint otherwise.
func voteBlockID(vote string) (present bool, blockID string) {
	if vote == "" || strings.Contains(vote, "nil-Vote") {
		return false, ""
	}
	m := voteBlockRe.FindStringSubmatch(vote)
	if m == nil {
		// Present but unparseable — count as participating, unknown target.
		return true, ""
	}
	tok := m[1]
	if strings.Trim(tok, "0") == "" { // all zeros => nil block
		return true, ""
	}
	return true, tok
}

// first hex token after the "(Prevote)"/"(Precommit)" marker's closing paren.
var voteBlockRe = regexp.MustCompile(`\)\s+([0-9A-Fa-f]+)`)

// power fraction is the trailing "= 0.84" of a bit-array string:
//
//	"BA{57:xx_x...} 1234/1500 = 0.82"
var powerFracRe = regexp.MustCompile(`=\s*([0-9.]+)\s*$`)

func powerFrac(bitArray string) float64 {
	m := powerFracRe.FindStringSubmatch(strings.TrimSpace(bitArray))
	if m == nil {
		return 0
	}
	var f float64
	fmt.Sscan(m[1], &f)
	return f
}
