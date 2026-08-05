package server

import (
	"context"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

// Data loss is inferred from repair's own failures rather than from a separate
// probe pass.
//
// A probe sweep asks every candidate host "do you have this?" in one burst, so
// a network partition or a bad five minutes looks identical to content being
// gone. Repair failing across many cycles on different days is far stronger
// evidence, and it costs nothing extra: repair is already attempting these
// pulls. It is also a stricter test, since a real pull failing beats a peer's
// blob-info endpoint answering "no".
//
// The volume is small. On a production store-all node, ~1.03M pulls produced
// only ~248 CIDs that failed against every host, so tracking complete failures
// is a few hundred rows rather than a hot-path write problem. Per-host failures
// are not tracked -- only the case where nothing on the network served it.

const (
	// dataLossMinCycles is how many separate repair cycles must fail before a
	// CID is declared lost. Each cycle visits a CID at most once, so this
	// counts distinct attempts rather than retries within one pass.
	dataLossMinCycles = 5

	// dataLossMinElapsed is how long failures must persist. Cycles alone are
	// not evidence: OPENAUDIO_REPAIR_INTERVAL is operator-tunable and is 5m on
	// some nodes, where five cycles is twenty-five minutes -- nowhere near
	// enough to distinguish loss from an outage.
	dataLossMinElapsed = 7 * 24 * time.Hour

	// dataLossMinHealthyPeers is how many peers a run must have seen before its
	// failures count. A run limping along against two reachable peers is not
	// evidence that the network lacks content.
	dataLossMinHealthyPeers = 5

	// dataLossRecheckAfter is how long a declared loss is skipped before repair
	// tries once more. Content does come back -- an operator restores a backup,
	// a long-dead node returns -- and a permanent skip would never notice.
	//
	// The record is not expired: a failed recheck pushes this date forward and
	// leaves declared_at alone, so an already-known loss never re-reports as a
	// new one.
	dataLossRecheckAfter = 30 * 24 * time.Hour
)

// recordRepairFailure notes that a CID could not be pulled from any host this
// cycle, and declares it lost once the evidence is strong enough.
//
// Failures from an unhealthy run are dropped entirely rather than recorded,
// since they say more about this node's connectivity than about the network.
func (ss *MediorumServer) recordRepairFailure(ctx context.Context, cid string, tracker *RepairTracker) {
	if tracker.HealthyPeerCount < dataLossMinHealthyPeers {
		tracker.mu.Lock()
		tracker.Counters["data_loss_evidence_discarded"]++
		tracker.mu.Unlock()
		return
	}

	var (
		cycles     int
		firstFail  time.Time
		declaredAt *time.Time
	)
	err := ss.pgPool.QueryRow(ctx, `
		insert into repair_data_loss (cid, failed_cycles)
		values ($1, 1)
		on conflict (cid) do update
		   set failed_cycles = repair_data_loss.failed_cycles + 1,
		       last_failed_at = now(),
		       recovered_at = null
		returning failed_cycles, first_failed_at, declared_at`,
		cid).Scan(&cycles, &firstFail, &declaredAt)
	if err != nil {
		ss.logger.Warn("could not record repair failure", zap.String("cid", cid), zap.Error(err))
		return
	}

	// Already declared: this is a failed recheck. Push the next attempt out and
	// leave declared_at untouched so it is not counted as a new loss.
	if declaredAt != nil {
		_, err := ss.pgPool.Exec(ctx,
			`update repair_data_loss set recheck_after = now() + $2::interval where cid = $1`,
			cid, dataLossRecheckAfter.String())
		if err != nil {
			ss.logger.Warn("could not reschedule data loss recheck", zap.String("cid", cid), zap.Error(err))
		}
		tracker.mu.Lock()
		tracker.Counters["data_loss_recheck_failed"]++
		tracker.mu.Unlock()
		return
	}

	if cycles < dataLossMinCycles || time.Since(firstFail) < dataLossMinElapsed {
		return
	}

	_, err = ss.pgPool.Exec(ctx, `
		update repair_data_loss
		   set declared_at = now(), recheck_after = now() + $2::interval
		 where cid = $1 and declared_at is null`,
		cid, dataLossRecheckAfter.String())
	if err != nil {
		ss.logger.Warn("could not declare data loss", zap.String("cid", cid), zap.Error(err))
		return
	}

	tracker.mu.Lock()
	tracker.Counters["data_loss_declared"]++
	tracker.mu.Unlock()
	ss.logger.Warn("declaring data loss: no host served this CID",
		zap.String("cid", cid),
		zap.Int("failedCycles", cycles),
		zap.Duration("over", time.Since(firstFail).Truncate(time.Hour)))
}

// clearRepairFailure records that a CID was pulled successfully. For a
// previously declared loss this is a recovery -- worth surfacing, since it
// means content returned to the network.
func (ss *MediorumServer) clearRepairFailure(ctx context.Context, cid string, tracker *RepairTracker) {
	var declared *time.Time
	err := ss.pgPool.QueryRow(ctx, `
		update repair_data_loss
		   set recovered_at = now(), recheck_after = null
		 where cid = $1 and recovered_at is null
		returning declared_at`, cid).Scan(&declared)
	if err != nil {
		return // no row: the overwhelmingly common case, nothing to clear
	}
	if declared != nil {
		tracker.mu.Lock()
		tracker.Counters["data_loss_recovered"]++
		tracker.mu.Unlock()
		ss.logger.Info("previously lost CID recovered", zap.String("cid", cid))
	}
}

// loadRepairSkips returns CIDs repair should not attempt this cycle: prune
// decisions plus declared data loss that is not yet due for a recheck.
func (ss *MediorumServer) loadRepairSkips(ctx context.Context) (map[string]struct{}, error) {
	skips, err := ss.loadPruneSkips(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := ss.pgPool.Query(ctx, `
		select cid from repair_data_loss
		 where declared_at is not null
		   and recovered_at is null
		   and (recheck_after is null or recheck_after > now())`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			return nil, err
		}
		skips[cid] = struct{}{}
	}
	return skips, rows.Err()
}

// DataLossSummary is the operator-facing view. Total only grows when a
// genuinely new CID is declared, so a healthy node reports the same figure
// indefinitely with zero new losses.
type DataLossSummary struct {
	Total          int64      `json:"total"`
	PendingRecheck int64      `json:"pendingRecheck"`
	Recovered      int64      `json:"recovered"`
	Accumulating   int64      `json:"accumulating"`
	DeclaredLast24 int64      `json:"declaredLast24h"`
	OldestDeclared *time.Time `json:"oldestDeclared,omitempty"`
	NewestDeclared *time.Time `json:"newestDeclared,omitempty"`
}

func (ss *MediorumServer) serveDataLoss(c echo.Context) error {
	var s DataLossSummary
	err := ss.pgPool.QueryRow(c.Request().Context(), `
		select
		  count(*) filter (where declared_at is not null and recovered_at is null),
		  count(*) filter (where declared_at is not null and recovered_at is null
		                     and recheck_after is not null and recheck_after <= now()),
		  count(*) filter (where recovered_at is not null),
		  count(*) filter (where declared_at is null and recovered_at is null),
		  count(*) filter (where declared_at > now() - interval '24 hours'),
		  min(declared_at) filter (where recovered_at is null),
		  max(declared_at) filter (where recovered_at is null)
		from repair_data_loss`).
		Scan(&s.Total, &s.PendingRecheck, &s.Recovered, &s.Accumulating,
			&s.DeclaredLast24, &s.OldestDeclared, &s.NewestDeclared)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, s)
}
