package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func healthyTracker() *RepairTracker {
	return &RepairTracker{
		StartedAt:        time.Now(),
		Counters:         map[string]int{},
		HealthyPeerCount: dataLossMinHealthyPeers,
		mu:               newTrackerMutex(),
	}
}

func dataLossRow(t *testing.T, ss *MediorumServer, cid string) (cycles int, declared *time.Time, recheck *time.Time, recovered *time.Time) {
	t.Helper()
	require.NoError(t, ss.pgPool.QueryRow(context.Background(),
		`select failed_cycles, declared_at, recheck_after, recovered_at from repair_data_loss where cid = $1`, cid).
		Scan(&cycles, &declared, &recheck, &recovered))
	return
}

func cleanupCID(t *testing.T, ss *MediorumServer, cid string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = ss.pgPool.Exec(context.Background(), `delete from repair_data_loss where cid = $1`, cid)
	})
}

// Failures accumulate but must not declare loss until BOTH thresholds are met.
// Cycles alone are meaningless when OPENAUDIO_REPAIR_INTERVAL is 5m.
func TestDataLossRequiresCyclesAndElapsedTime(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	cid := "QmDataLossThresholds"
	cleanupCID(t, ss, cid)

	for i := 0; i < dataLossMinCycles+2; i++ {
		ss.recordRepairFailure(ctx, cid, healthyTracker(), true)
	}

	cycles, declared, _, _ := dataLossRow(t, ss, cid)
	assert.Greater(t, cycles, dataLossMinCycles)
	assert.Nil(t, declared, "enough cycles but no elapsed time must not declare loss")

	// Backdate the first failure past the elapsed-time floor.
	_, err := ss.pgPool.Exec(ctx,
		`update repair_data_loss set first_failed_at = now() - $2::interval where cid = $1`,
		cid, (dataLossMinElapsed + time.Hour).String())
	require.NoError(t, err)

	tr := healthyTracker()
	ss.recordRepairFailure(ctx, cid, tr, true)
	_, declared, recheck, _ := dataLossRow(t, ss, cid)
	assert.NotNil(t, declared, "both thresholds met must declare loss")
	assert.NotNil(t, recheck, "a declared loss must be scheduled for recheck")
	assert.Equal(t, 1, tr.Counters["data_loss_declared"])
}

// The property that matters operationally: once declared, a CID must never be
// reported as newly lost again, however many times the recheck fails.
// Otherwise "data loss" looks like it keeps growing forever.
func TestDataLossIsDeclaredOnlyOnce(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	cid := "QmDataLossDeclaredOnce"
	cleanupCID(t, ss, cid)

	_, err := ss.pgPool.Exec(ctx, `
		insert into repair_data_loss (cid, failed_cycles, first_failed_at, declared_at, recheck_after)
		values ($1, $2, now() - interval '30 days', now() - interval '10 days', now() - interval '1 day')`,
		cid, dataLossMinCycles)
	require.NoError(t, err)

	_, declaredBefore, _, _ := dataLossRow(t, ss, cid)

	// Recheck comes due and fails again, several times.
	for i := 0; i < 3; i++ {
		tr := healthyTracker()
		ss.recordRepairFailure(ctx, cid, tr, true)
		assert.Zero(t, tr.Counters["data_loss_declared"],
			"a failed recheck must never count as a new loss")
		assert.Equal(t, 1, tr.Counters["data_loss_recheck_failed"])
	}

	_, declaredAfter, recheck, _ := dataLossRow(t, ss, cid)
	assert.Equal(t, declaredBefore.UTC(), declaredAfter.UTC(),
		"declared_at is the identity of the loss and must never move")
	require.NotNil(t, recheck)
	assert.True(t, recheck.After(time.Now()), "a failed recheck must push the next attempt out")
}

// A run that could barely reach the network says nothing about whether content
// exists, so its failures must not be recorded at all.
func TestDataLossIgnoresUnhealthyRuns(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	cid := "QmDataLossUnhealthyRun"
	cleanupCID(t, ss, cid)

	tr := healthyTracker()
	tr.HealthyPeerCount = dataLossMinHealthyPeers - 1
	ss.recordRepairFailure(ctx, cid, tr, true)

	var n int
	require.NoError(t, ss.pgPool.QueryRow(ctx,
		`select count(*) from repair_data_loss where cid = $1`, cid).Scan(&n))
	assert.Zero(t, n, "failures from an unhealthy run must not be recorded")
	assert.Equal(t, 1, tr.Counters["data_loss_evidence_discarded"])
}

// Content coming back is a real event and must clear the loss.
func TestDataLossRecovery(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	cid := "QmDataLossRecovered"
	cleanupCID(t, ss, cid)

	_, err := ss.pgPool.Exec(ctx, `
		insert into repair_data_loss (cid, failed_cycles, declared_at, recheck_after)
		values ($1, 9, now() - interval '5 days', now() - interval '1 hour')`, cid)
	require.NoError(t, err)

	tr := healthyTracker()
	ss.clearRepairFailure(ctx, cid, tr)

	_, _, _, recovered := dataLossRow(t, ss, cid)
	assert.NotNil(t, recovered, "a successful pull must clear the loss")
	assert.Equal(t, 1, tr.Counters["data_loss_recovered"])
}

// A declared loss is skipped until its recheck comes due, then offered again.
func TestLoadRepairSkipsHonorsRecheckWindow(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	pending := "QmSkipPendingRecheck" + fmt.Sprint(time.Now().UnixNano())
	due := "QmSkipRecheckDue" + fmt.Sprint(time.Now().UnixNano())
	cleanupCID(t, ss, pending)
	cleanupCID(t, ss, due)

	_, err := ss.pgPool.Exec(ctx, `
		insert into repair_data_loss (cid, declared_at, recheck_after) values
		  ($1, now(), now() + interval '10 days'),
		  ($2, now(), now() - interval '1 hour')`, pending, due)
	require.NoError(t, err)

	skips, err := ss.loadRepairSkips(ctx)
	require.NoError(t, err)

	_, pendingSkipped := skips[pending]
	_, dueSkipped := skips[due]
	assert.True(t, pendingSkipped, "a loss not yet due for recheck must be skipped")
	assert.False(t, dueSkipped, "a loss past its recheck date must be retried")
}

// The cap in #436 means a normal failed pull only proves ~10 of ~68 hosts came
// up empty. That is not grounds for declaring the network has lost content, so
// declaration must wait for a sweep of the full ranking.
func TestDataLossRequiresExhaustiveSweep(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	cid := "QmDataLossNeedsFullSweep"
	cleanupCID(t, ss, cid)

	// Both thresholds satisfied, but every attempt was the bounded one.
	_, err := ss.pgPool.Exec(ctx, `
		insert into repair_data_loss (cid, failed_cycles, first_failed_at)
		values ($1, $2, now() - $3::interval)`,
		cid, dataLossMinCycles, (dataLossMinElapsed + time.Hour).String())
	require.NoError(t, err)

	tr := healthyTracker()
	ss.recordRepairFailure(ctx, cid, tr, false)

	_, declared, _, _ := dataLossRow(t, ss, cid)
	assert.Nil(t, declared, "a bounded pull must never authorise declaring data loss")
	assert.Equal(t, 1, tr.Counters["data_loss_awaiting_full_sweep"])

	// The same failure, this time from a full-network sweep.
	tr = healthyTracker()
	ss.recordRepairFailure(ctx, cid, tr, true)

	_, declared, _, _ = dataLossRow(t, ss, cid)
	assert.NotNil(t, declared, "an exhaustive sweep that found nothing may declare loss")
	assert.Equal(t, 1, tr.Counters["data_loss_declared"])
}

// Escalation must kick in before the declaration threshold, so the cycles
// leading up to a declaration are full sweeps rather than bounded samples.
func TestDataLossEscalatesBeforeDeclaring(t *testing.T) {
	assert.Less(t, dataLossEscalateAfter, dataLossMinCycles,
		"escalation must precede declaration, or loss is declared on bounded evidence")

	tracker := &RepairTracker{FailingCIDs: map[string]int{"QmHot": dataLossEscalateAfter}}
	assert.Equal(t, dataLossEscalateAfter, tracker.failedCycles("QmHot"))
	assert.Zero(t, tracker.failedCycles("QmUnknown"), "an unseen CID must not escalate")

	var nilTracker RepairTracker
	assert.Zero(t, nilTracker.failedCycles("QmAny"), "nil map must not panic")
}

// Only CIDs still accumulating evidence need escalation; declared and
// recovered ones must not be reloaded into the hot path every cycle.
func TestLoadFailingCIDsExcludesSettledRecords(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	suffix := fmt.Sprint(time.Now().UnixNano())
	accumulating := "QmAccumulating" + suffix
	declared := "QmAlreadyDeclared" + suffix
	recovered := "QmAlreadyRecovered" + suffix
	for _, c := range []string{accumulating, declared, recovered} {
		cleanupCID(t, ss, c)
	}

	_, err := ss.pgPool.Exec(ctx, `
		insert into repair_data_loss (cid, failed_cycles, declared_at, recovered_at) values
		  ($1, 4, null, null),
		  ($2, 9, now(), null),
		  ($3, 2, null, now())`, accumulating, declared, recovered)
	require.NoError(t, err)

	failing, err := ss.loadFailingCIDs(ctx)
	require.NoError(t, err)

	assert.Equal(t, 4, failing[accumulating])
	assert.NotContains(t, failing, declared, "a declared loss is skipped, not escalated")
	assert.NotContains(t, failing, recovered, "a recovered CID has no failures to escalate")
}
