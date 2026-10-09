package server

import (
	"testing"
	"time"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// roundTripRollup sends a rollup through the same encoding a proposal takes
// between PrepareProposal and ProcessProposal.
func roundTripRollup(t *testing.T, rollup *v1.SlaRollup) *v1.SlaRollup {
	t.Helper()
	b, err := proto.Marshal(&v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_SlaRollup{SlaRollup: rollup},
	})
	require.NoError(t, err)
	var decoded v1.SignedTransaction
	require.NoError(t, proto.Unmarshal(b, &decoded))
	return decoded.GetSlaRollup()
}

func TestRollupsMatch(t *testing.T) {
	ts := timestamppb.New(time.Date(2026, 8, 26, 3, 43, 54, 613043000, time.UTC))
	newRollup := func(reports ...*v1.SlaNodeReport) *v1.SlaRollup {
		// mirrors createRollup, which always allocates a non-nil slice
		r := make([]*v1.SlaNodeReport, 0, len(reports))
		return &v1.SlaRollup{Timestamp: ts, BlockStart: 0, BlockEnd: 5839, Reports: append(r, reports...)}
	}
	report := func(addr string, n int32) *v1.SlaNodeReport {
		return &v1.SlaNodeReport{Address: addr, NumBlocksProposed: n}
	}

	t.Run("no reports survives the proposal round trip", func(t *testing.T) {
		proposed := roundTripRollup(t, newRollup())
		require.Nil(t, proposed.Reports, "proto decodes an empty repeated field as nil")
		require.True(t, rollupsMatch(newRollup(), proposed))
	})

	t.Run("matching reports survive the proposal round trip", func(t *testing.T) {
		mine := newRollup(report("A", 3), report("B", 0))
		proposed := roundTripRollup(t, newRollup(report("A", 3), report("B", 0)))
		require.True(t, rollupsMatch(mine, proposed))
	})

	t.Run("different reports are rejected", func(t *testing.T) {
		mine := newRollup(report("A", 3))
		proposed := roundTripRollup(t, newRollup(report("A", 4)))
		require.False(t, rollupsMatch(mine, proposed))
	})

	t.Run("reports on only one side are rejected", func(t *testing.T) {
		require.False(t, rollupsMatch(newRollup(report("A", 0)), roundTripRollup(t, newRollup())))
		require.False(t, rollupsMatch(newRollup(), roundTripRollup(t, newRollup(report("A", 0)))))
	})

	t.Run("different block range or timestamp is rejected", func(t *testing.T) {
		proposed := roundTripRollup(t, newRollup())
		proposed.BlockEnd = 5838
		require.False(t, rollupsMatch(newRollup(), proposed))

		proposed = roundTripRollup(t, newRollup())
		proposed.BlockStart = 1
		require.False(t, rollupsMatch(newRollup(), proposed))

		proposed = roundTripRollup(t, newRollup())
		proposed.Timestamp = timestamppb.New(ts.AsTime().Add(time.Nanosecond))
		require.False(t, rollupsMatch(newRollup(), proposed))
	})
}
