package pages

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- mock builders ------------------------------------------------------------
//
// These construct a CometBFT `round_state` payload (the shape returned by the
// /consensus_state RPC) so the analyzer can be exercised across realistic states
// without a live node.

// buildVote renders a single CometBFT vote string. kind is one of:
//
//	"absent"  -> validator has not voted (nil-Vote)
//	"nil"     -> voted for nil block (block hash all-zeros)
//	<hexhash> -> voted for that block hash
func buildVote(i int, kind, voteType string) string {
	if kind == "absent" {
		return "nil-Vote"
	}
	hash := "000000000000"
	if kind != "nil" {
		hash = kind
	}
	return fmt.Sprintf(
		"Vote{%d:0B6BADE75A38 100/00/SIGNED_MSG_TYPE_%s(%s) %s 2C33076B33B8 9BC7AF1896D1 @ 2026-07-14T20:17:10.8Z}",
		i, strings.ToUpper(voteType), voteType, hash,
	)
}

// buildRoundState builds a round_state JSON blob. rounds[r] is the per-validator
// vote outcome for round r (same outcome used for prevote and precommit).
func buildRoundState(hrs string, rounds [][]string, prevoteFrac, precommitFrac float64) []byte {
	rs := rawRoundState{HRS: hrs, StartTime: time.Now()}
	for r, votes := range rounds {
		vs := rawVoteSet{Round: r}
		for i, k := range votes {
			vs.Prevotes = append(vs.Prevotes, buildVote(i, k, "Prevote"))
			vs.Precommits = append(vs.Precommits, buildVote(i, k, "Precommit"))
		}
		vs.PrevotesBitArray = fmt.Sprintf("BA{4:xxxx} 40/48 = %.2f", prevoteFrac)
		vs.PrecommitsBitArray = fmt.Sprintf("BA{4:xxxx} 40/48 = %.2f", precommitFrac)
		rs.HeightVoteSet = append(rs.HeightVoteSet, vs)
	}
	b, _ := json.Marshal(rs)
	return b
}

func repeat(kind string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = kind
	}
	return out
}

// --- classification tests -----------------------------------------------------

func TestAnalyzeConsensusState_Classification(t *testing.T) {
	now := time.Date(2026, 7, 14, 21, 30, 0, 0, time.UTC)
	th := DefaultHaltThresholds()

	cases := []struct {
		name       string
		json       []byte
		lastBlock  time.Time
		catchingUp bool
		want       ConsensusClassification
		wantHalt   bool
	}{
		{
			name:      "healthy: round 0, recent block, one block hash",
			json:      buildRoundState("100/0/8", [][]string{repeat("AAAABBBBCCCC", 46)}, 0.86, 0.84),
			lastBlock: now.Add(-2 * time.Second),
			want:      ConsensusHealthy,
			wantHalt:  false,
		},
		{
			name:      "degraded: elevated round, still recent block",
			json:      buildRoundState("100/4/6", [][]string{repeat("AAAABBBBCCCC", 46)}, 0.80, 0.80),
			lastBlock: now.Add(-10 * time.Second),
			want:      ConsensusDegraded,
			wantHalt:  false,
		},
		{
			name: "halt liveness: all nil, stale block, high round (prod repro)",
			json: buildRoundState("28173293/23/3", [][]string{
				repeat("nil", 48),
				repeat("nil", 47),
				repeat("nil", 44),
			}, 0.84, 0.86),
			lastBlock: now.Add(-70 * time.Minute),
			want:      ConsensusHaltLiveness,
			wantHalt:  true,
		},
		{
			name: "halt split: two competing hashes in a round",
			json: buildRoundState("500/12/6", [][]string{
				append(repeat("AAAAAAAAAAAA", 24), repeat("BBBBBBBBBBBB", 24)...),
			}, 0.84, 0.50),
			lastBlock: now.Add(-3 * time.Minute),
			want:      ConsensusHaltSplit,
			wantHalt:  true,
		},
		{
			name:       "catching up: suppresses halt even at high round",
			json:       buildRoundState("100/40/3", [][]string{repeat("nil", 48)}, 0.10, 0.10),
			lastBlock:  now.Add(-2 * time.Hour),
			catchingUp: true,
			want:       ConsensusCatchingUp,
			wantHalt:   false,
		},
		{
			name:      "halt by round only: recent block but round >= HighRound",
			json:      buildRoundState("100/10/3", [][]string{repeat("nil", 48)}, 0.84, 0.84),
			lastBlock: now.Add(-5 * time.Second),
			want:      ConsensusHaltLiveness,
			wantHalt:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, err := AnalyzeConsensusState(tc.json, tc.lastBlock, tc.catchingUp, now, th)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if h.Classification != tc.want {
				t.Errorf("classification = %q, want %q (summary: %s)", h.Classification, tc.want, h.Summary)
			}
			if h.Halted != tc.wantHalt {
				t.Errorf("halted = %v, want %v", h.Halted, tc.wantHalt)
			}
		})
	}
}

func TestAnalyzeConsensusState_ParticipationAndBreakdown(t *testing.T) {
	now := time.Date(2026, 7, 14, 21, 30, 0, 0, time.UTC)

	// 48 validators: 40 vote nil, 8 absent, across a single round.
	votes := append(repeat("nil", 40), repeat("absent", 8)...)
	js := buildRoundState("28173293/23/3", [][]string{votes}, 0.84, 0.86)

	h, err := AnalyzeConsensusState(js, now.Add(-time.Hour), false, now, DefaultHaltThresholds())
	if err != nil {
		t.Fatal(err)
	}
	if h.TotalValidators != 48 {
		t.Errorf("total validators = %d, want 48", h.TotalValidators)
	}
	if h.VotingValidators != 40 {
		t.Errorf("voting validators = %d, want 40", h.VotingValidators)
	}
	if h.NilPrevotes != 40 || h.BlockPrevotes != 0 {
		t.Errorf("nil=%d block=%d, want nil=40 block=0", h.NilPrevotes, h.BlockPrevotes)
	}
	if h.PrevotePowerFrac != 0.84 {
		t.Errorf("prevote power frac = %v, want 0.84", h.PrevotePowerFrac)
	}
	if h.Height != 28173293 || h.Round != 23 {
		t.Errorf("height/round = %d/%d, want 28173293/23", h.Height, h.Round)
	}
}

func TestAnalyzeConsensusState_BadJSON(t *testing.T) {
	_, err := AnalyzeConsensusState([]byte("not json"), time.Now(), false, time.Now(), DefaultHaltThresholds())
	if err == nil {
		t.Fatal("expected error for malformed round state")
	}
}

// --- parsing unit tests -------------------------------------------------------

func TestVoteBlockID(t *testing.T) {
	cases := []struct {
		vote        string
		wantPresent bool
		wantBlock   string
	}{
		{"nil-Vote", false, ""},
		// real prod vote for nil (block hash all zeros)
		{"Vote{7:326D405ABA6E 28173293/00/SIGNED_MSG_TYPE_PREVOTE(Prevote) 000000000000 9BC7AF1896D1 000000000000 @ 2026-07-14T20:17:10.8Z}", true, ""},
		// vote for a real block
		{"Vote{7:326D405ABA6E 28173293/00/SIGNED_MSG_TYPE_PRECOMMIT(Precommit) 2C33076B33B8 9BC7AF1896D1 000000000000 @ 2026-07-14T20:17:10.8Z}", true, "2C33076B33B8"},
	}
	for _, tc := range cases {
		present, block := voteBlockID(tc.vote)
		if present != tc.wantPresent || block != tc.wantBlock {
			t.Errorf("voteBlockID(%q) = (%v,%q), want (%v,%q)", tc.vote, present, block, tc.wantPresent, tc.wantBlock)
		}
	}
}

func TestPowerFrac(t *testing.T) {
	cases := map[string]float64{
		"BA{57:xx_xx} 12345/18000 = 0.84": 0.84,
		"BA{4:____} 0/48 = 0.00":          0.0,
		"garbage":                         0.0,
	}
	for in, want := range cases {
		if got := powerFrac(in); got != want {
			t.Errorf("powerFrac(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestShowPanel(t *testing.T) {
	cases := []struct {
		class ConsensusClassification
		halt  bool
		want  bool
	}{
		{ConsensusHealthy, false, false},
		{ConsensusCatchingUp, false, false},
		{ConsensusUnknown, false, false},
		{ConsensusDegraded, false, true}, // early warning — shown
		{ConsensusHaltLiveness, true, true},
		{ConsensusHaltSplit, true, true},
	}
	for _, tc := range cases {
		h := &ConsensusHealth{Classification: tc.class, Halted: tc.halt}
		if got := h.ShowPanel(); got != tc.want {
			t.Errorf("%s (halted=%v): ShowPanel()=%v, want %v", tc.class, tc.halt, got, tc.want)
		}
	}
	if (*ConsensusHealth)(nil).ShowPanel() {
		t.Error("nil health should not show panel")
	}
	if UnknownConsensusHealth().ShowPanel() {
		t.Error("unknown health should not show panel")
	}
}

func TestParseHRS(t *testing.T) {
	h, r, step := parseHRS("28173293/23/3")
	if h != 28173293 || r != 23 || step != "Propose" {
		t.Errorf("parseHRS = (%d,%d,%q), want (28173293,23,Propose)", h, r, step)
	}
	if _, _, s := parseHRS("garbage"); s != "unknown" {
		t.Errorf("parseHRS(garbage) step = %q, want unknown", s)
	}
}
