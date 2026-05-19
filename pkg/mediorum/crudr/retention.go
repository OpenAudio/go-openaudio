package crudr

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/env"
	"github.com/oklog/ulid/v2"
	"go.uber.org/zap"
)

// RetentionConfig holds knobs for the ops retention sweep and the one-time
// dormant-table cleanup. Configuration is read from environment variables in
// LoadRetentionConfig, with safe defaults.
//
// Lifecycle:
//   - DormantCleanupEnabled defaults to true. The cleanup runs once per
//     process start; re-running is a no-op once the table has been cleaned.
//     Set OPENAUDIO_MEDIORUM_KEEP_DORMANT_OPS=true to opt out.
//   - RetentionDays==0 disables the ongoing retention sweep (archive mode,
//     current default behavior). Setting OPENAUDIO_MEDIORUM_OPS_RETENTION_DAYS
//     to a positive integer enables the sweep.
type RetentionConfig struct {
	// DormantCleanupEnabled controls Component 1 (one-time dormant-table cleanup).
	DormantCleanupEnabled bool
	// DormantThreshold is the minimum age of the newest op for a table to be
	// considered dormant. The default mirrors the structurally-dormant signal
	// observed in qm_audio_analyses (no producer writes since Nov 2025).
	DormantThreshold time.Duration

	// RetentionDays controls Component 3 (ongoing retention sweep). Zero
	// disables the sweep (archive mode). Positive values delete ops older
	// than this many days, subject to the cursor floor.
	RetentionDays int
	// SweepInterval is the cadence of the ongoing retention sweep loop.
	SweepInterval time.Duration
	// SweepBatchLimit is the maximum number of ops deleted in a single
	// DELETE statement. Keeps long-running transactions short and avoids
	// blocking concurrent ops writes.
	SweepBatchLimit int
	// CursorSafetyMargin is subtracted from min(cursors.last_ulid) before
	// computing the cutoff. Gives the slowest reachable peer time to catch
	// up between sweeps.
	CursorSafetyMargin time.Duration
}

// LoadRetentionConfig reads the retention configuration from environment
// variables. All fields use OPENAUDIO_ canonical names.
func LoadRetentionConfig() RetentionConfig {
	cfg := RetentionConfig{
		DormantCleanupEnabled: !env.Bool("OPENAUDIO_MEDIORUM_KEEP_DORMANT_OPS"),
		DormantThreshold:      env.GetDuration(90*24*time.Hour, "OPENAUDIO_MEDIORUM_DORMANT_OPS_THRESHOLD"),
		RetentionDays:         env.GetInt(0, "OPENAUDIO_MEDIORUM_OPS_RETENTION_DAYS"),
		SweepInterval:         env.GetDuration(1*time.Hour, "OPENAUDIO_MEDIORUM_OPS_RETENTION_SWEEP_INTERVAL"),
		SweepBatchLimit:       env.GetInt(10000, "OPENAUDIO_MEDIORUM_OPS_RETENTION_BATCH_LIMIT"),
		CursorSafetyMargin:    env.GetDuration(1*time.Hour, "OPENAUDIO_MEDIORUM_OPS_RETENTION_CURSOR_MARGIN"),
	}
	return cfg
}

// RetentionStats are atomic counters exposed for operator-visible metrics.
type RetentionStats struct {
	// DormantTablesCleaned counts tables whose ops were dropped by the one-time
	// dormant-table cleanup during this process lifetime.
	DormantTablesCleaned atomic.Uint64
	// DormantOpsDeleted counts the total number of ops rows the dormant-table
	// cleanup deleted during this process lifetime.
	DormantOpsDeleted atomic.Uint64
	// RetentionOpsDeleted counts ops rows deleted by the ongoing retention
	// sweep during this process lifetime.
	RetentionOpsDeleted atomic.Uint64
	// RetentionSweepsSkipped counts sweep ticks where no rows were eligible
	// for deletion (empty cursor blocked the cutoff, no rows older than
	// MinAge, etc).
	RetentionSweepsSkipped atomic.Uint64
	// SweepGapAdvances counts the number of times the local sweep client
	// observed a retention-gap signal from a peer and explicitly advanced
	// its cursor across the gap. This is the operator-visible metric
	// for Topic-7 silent-skip detection.
	SweepGapAdvances atomic.Uint64
}

// retention is the package-level set of stats, exposed for tests and
// operator metrics. Values reset only on process restart.
var retention RetentionStats

// Stats returns a pointer to the package retention stats. The fields are
// atomic counters and may be read concurrently with the running sweep.
func Stats() *RetentionStats { return &retention }

// MinAvailableULID returns the smallest ulid currently stored in the ops
// table. Returns ("", nil) when the ops table is empty. Callers use this
// to advertise the retention floor (see ServeCrudSweep gap signal) and to
// short-circuit retention work when the table is already empty.
func (c *Crudr) MinAvailableULID(ctx context.Context) (string, error) {
	var out sql.NullString
	err := c.DB.WithContext(ctx).
		Raw(`SELECT MIN(ulid) FROM ops`).
		Scan(&out).Error
	if err != nil {
		return "", err
	}
	if !out.Valid {
		return "", nil
	}
	return out.String, nil
}

// minDormantThreshold is the smallest dormancy window we will honor. An
// operator who sets OPENAUDIO_MEDIORUM_DORMANT_OPS_THRESHOLD to a value
// below this is treated as misconfigured and the threshold is clamped up.
// 24h is a wide enough floor that a temporarily-quiet table is never
// misclassified as dormant during a brief lull.
const minDormantThreshold = 24 * time.Hour

// dormantBatchSize bounds the per-statement work the dormant cleanup
// does. A single all-rows DELETE on a 50M-row dormant table holds locks
// and WAL for the entire delete; batching keeps each transaction small
// enough that concurrent op writes for OTHER tables aren't blocked by
// the cleanup, and an OOM-kill mid-cleanup rolls back at most one
// batch's worth of work.
const dormantBatchSize = 10000

// CleanupDormantOps drops ops rows for tables that have not received a write
// in cfg.DormantThreshold. It is idempotent: re-running is a no-op once a
// table has been cleaned (the newest remaining op is now from this process's
// write traffic, not the historical sediment).
//
// The set of registered tables comes from c.typeMap, so a table that is no
// longer registered by mediorum (e.g. removed in a future PR) cannot be
// misclassified as dormant by this function. We only delete ops for tables
// the caller has registered with RegisterModels.
//
// Gap-signal interaction. The ServeCrudSweep gap signal advertises
// MIN(ulid) across ALL tables in ops, not per-table. When this cleanup
// removes a fully-dormant table's ops but other tables still hold older
// ops (the val001 baseline: `uploads` rows back to 2023-03 outlast
// `qm_audio_analyses` ops that were emitted in 2024), the overall
// min(ulid) does not change. A peer whose cursor falls between the
// dormant table's oldest and newest ulids would, on next sweep, silently
// skip the deleted dormant-table ops because the gap signal only fires
// against the overall floor. This is acceptable specifically for dormant
// tables because, by construction, their producer code has been removed
// (no live consumer depends on the deleted ops). New maintainers who add
// a CRUD table that retains both an active producer and a low write
// cadence (e.g., quarterly metrics) should not rely on the gap signal
// to cover this case — keep such tables out of CRUDR or out-of-band.
//
// Future-maintainer note: any new CRUD table added via RegisterModels must
// expect to be wiped here if it sees no writes for cfg.DormantThreshold
// (default 90d). A legitimate low-write-cadence table (e.g. a quarterly
// metric) belongs outside the CRUD layer or behind a non-default
// threshold; the dormancy default is calibrated for upload/audio-analysis-
// rate write streams.
//
// Deletes are batched so a multi-million-row dormant table does not hold
// locks or WAL for one giant transaction. Each batch is its own statement
// bounded by `ulid < cutoffULID`; that guard prevents the race where a
// producer writes a new op for the table after the dormancy check but
// before the batched DELETE catches up — the new op's ulid sits above the
// cutoff and is never touched. An interrupted cleanup leaves a well-formed
// table with fewer dormant rows, and the next run picks up where the
// previous left off.
//
// Returns the per-table deletion counts and a non-nil error only if a DB
// operation fails. A no-op clean (cfg disabled or no dormant tables) is a
// nil-error empty-map return.
func (c *Crudr) CleanupDormantOps(ctx context.Context, cfg RetentionConfig) (map[string]int64, error) {
	deleted := map[string]int64{}
	if !cfg.DormantCleanupEnabled {
		c.logger.Info("dormant ops cleanup disabled by OPENAUDIO_MEDIORUM_KEEP_DORMANT_OPS")
		return deleted, nil
	}

	threshold := cfg.DormantThreshold
	if threshold < minDormantThreshold {
		c.logger.Warn("dormant threshold below safety floor; clamping",
			zap.Duration("configured", cfg.DormantThreshold),
			zap.Duration("floor", minDormantThreshold))
		threshold = minDormantThreshold
	}

	// Snapshot the currently registered table set under the mutex so a
	// concurrent RegisterModels call cannot race with the scan below.
	c.mu.Lock()
	tables := make([]string, 0, len(c.typeMap))
	for t := range c.typeMap {
		tables = append(tables, t)
	}
	c.mu.Unlock()

	cutoff := time.Now().Add(-threshold)
	cutoffULID, err := ulidAtTime(cutoff)
	if err != nil {
		return deleted, fmt.Errorf("compute dormant cutoff: %w", err)
	}

	for _, table := range tables {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}

		// Find the most recent op for this table. NULL => no ops at all,
		// which means the table is either brand-new or has already been
		// cleaned; either way, skip.
		var maxULID sql.NullString
		err := c.DB.WithContext(ctx).
			Raw(`SELECT MAX(ulid) FROM ops WHERE "table" = ?`, table).
			Scan(&maxULID).Error
		if err != nil {
			return deleted, fmt.Errorf("query max ulid for %s: %w", table, err)
		}
		if !maxULID.Valid || maxULID.String == "" {
			c.logger.Debug("dormant cleanup: no ops for table",
				zap.String("table", table))
			continue
		}

		// Compare lex order against the cutoff. ULIDs sort
		// chronologically, so newest_op < cutoff_ulid means the newest op
		// is older than the cutoff, i.e. the table is dormant.
		if maxULID.String >= cutoffULID {
			c.logger.Debug("dormant cleanup: table still active",
				zap.String("table", table),
				zap.String("newest_op_ulid", maxULID.String),
				zap.String("cutoff_ulid", cutoffULID))
			continue
		}

		// Batched DELETE bounded by the dormancy cutoff. Each iteration
		// takes one bounded chunk and commits, so a multi-million-row
		// dormant table is cleaned in pieces. The `ulid < cutoffULID`
		// guard is load-bearing: if a producer writes a new op for this
		// table between the dormancy check above and a later batch (the
		// "dormant table just became active mid-cleanup" race), that new
		// op's ULID is above the cutoff and is preserved. Loop exits
		// when a batch returns zero rows.
		var totalForTable int64
		for {
			if err := ctx.Err(); err != nil {
				return deleted, err
			}
			res := c.DB.WithContext(ctx).Exec(`
				WITH victims AS MATERIALIZED (
					SELECT ulid
					FROM ops
					WHERE "table" = ? AND ulid < ?
					ORDER BY ulid ASC
					LIMIT ?
				)
				DELETE FROM ops WHERE ulid IN (SELECT ulid FROM victims)
			`, table, cutoffULID, dormantBatchSize)
			if res.Error != nil {
				return deleted, fmt.Errorf("delete dormant ops for %s: %w", table, res.Error)
			}
			totalForTable += res.RowsAffected
			if res.RowsAffected < dormantBatchSize {
				break
			}
		}
		if totalForTable > 0 {
			deleted[table] = totalForTable
			retention.DormantTablesCleaned.Add(1)
			retention.DormantOpsDeleted.Add(uint64(totalForTable))
			c.logger.Info("dormant cleanup: dropped ops for table",
				zap.String("table", table),
				zap.Int64("ops_deleted", totalForTable),
				zap.String("newest_op_ulid", maxULID.String),
				zap.Duration("threshold", threshold))
		}
	}
	return deleted, nil
}

// DryRunPlan describes what a real retention pass would do without
// executing any DELETE. It is the operator-facing preview surface for
// retention behavior. Counts are exact (computed against live ops state)
// but the on-disk size estimate is heap-only and excludes index/TOAST
// overhead; multiply by ~1.18 to approximate index+heap recovery for the
// `ops` relation on val001 (84 GiB total / 72 GiB heap).
type DryRunPlan struct {
	// DormantTables maps table name -> rows the dormant cleanup would
	// delete. Tables that are still active are omitted. Empty when the
	// dormant cleanup is disabled.
	DormantTables map[string]int64
	// DormantBytes is the sum of pg_column_size over the rows
	// DormantTables would delete. Heap-only.
	DormantBytes int64
	// RetentionRows is the count of rows the ongoing retention sweep
	// would delete on its next tick. Zero when RetentionDays <= 0.
	RetentionRows int64
	// RetentionBytes is the heap-only sum for those rows.
	RetentionBytes int64
	// RetentionSkipReason is the reason the retention sweep would
	// skip this tick (empty cursor, ops table empty, etc). Empty when
	// the sweep would proceed.
	RetentionSkipReason string
	// RetentionCutoffULID is the cutoff the retention sweep would use.
	// Empty when the sweep would skip.
	RetentionCutoffULID string
	// DormantCutoffULID is the cutoff the dormant cleanup would use.
	DormantCutoffULID string
}

// DryRunRetention computes what CleanupDormantOps and one retentionTick
// would do given cfg, without executing any DELETE. Use this from a
// debug endpoint, an `audius-ctl` subcommand, or operator scripting
// before flipping retention on.
//
// The query plan mirrors the real cleanup logic exactly so a dry-run
// followed by a real run sees the same counts (modulo writes that land
// between the two calls).
func (c *Crudr) DryRunRetention(ctx context.Context, cfg RetentionConfig) (DryRunPlan, error) {
	plan := DryRunPlan{DormantTables: map[string]int64{}}

	// Dormant-cleanup preview.
	if cfg.DormantCleanupEnabled {
		threshold := cfg.DormantThreshold
		if threshold < minDormantThreshold {
			threshold = minDormantThreshold
		}
		cutoff := time.Now().Add(-threshold)
		cutoffULID, err := ulidAtTime(cutoff)
		if err != nil {
			return plan, fmt.Errorf("compute dormant cutoff: %w", err)
		}
		plan.DormantCutoffULID = cutoffULID

		c.mu.Lock()
		tables := make([]string, 0, len(c.typeMap))
		for t := range c.typeMap {
			tables = append(tables, t)
		}
		c.mu.Unlock()

		for _, table := range tables {
			if err := ctx.Err(); err != nil {
				return plan, err
			}
			var maxULID sql.NullString
			if err := c.DB.WithContext(ctx).
				Raw(`SELECT MAX(ulid) FROM ops WHERE "table" = ?`, table).
				Scan(&maxULID).Error; err != nil {
				return plan, fmt.Errorf("query max ulid for %s: %w", table, err)
			}
			if !maxULID.Valid || maxULID.String == "" || maxULID.String >= cutoffULID {
				continue
			}
			// This table would be cleaned. Compute exact row count
			// and heap bytes.
			var count int64
			var bytes sql.NullInt64
			if err := c.DB.WithContext(ctx).
				Raw(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(ops.*)), 0)
				     FROM ops WHERE "table" = ? AND ulid < ?`,
					table, cutoffULID).
				Row().Scan(&count, &bytes); err != nil {
				return plan, fmt.Errorf("dryrun dormant count for %s: %w", table, err)
			}
			if count > 0 {
				plan.DormantTables[table] = count
				plan.DormantBytes += bytes.Int64
			}
		}
	}

	// Retention-sweep preview. We mirror computeRetentionCutoff but do
	// not execute any DELETE.
	if cfg.RetentionDays > 0 {
		cutoff, reason, err := c.computeRetentionCutoff(ctx, cfg)
		if err != nil {
			return plan, fmt.Errorf("compute retention cutoff: %w", err)
		}
		if cutoff == "" {
			plan.RetentionSkipReason = reason
		} else {
			plan.RetentionCutoffULID = cutoff
			var rows int64
			var bytes sql.NullInt64
			if err := c.DB.WithContext(ctx).
				Raw(`SELECT COUNT(*), COALESCE(SUM(pg_column_size(ops.*)), 0)
				     FROM ops WHERE ulid < ?`, cutoff).
				Row().Scan(&rows, &bytes); err != nil {
				return plan, fmt.Errorf("dryrun retention count: %w", err)
			}
			plan.RetentionRows = rows
			plan.RetentionBytes = bytes.Int64
		}
	}

	return plan, nil
}

// RunRetention runs the ongoing retention sweep loop until ctx is cancelled.
// It is a no-op when cfg.RetentionDays <= 0. The first sweep runs after one
// SweepInterval; this gives the lifecycle's other initialization (RegisterModels,
// peer cursor backfill) time to settle.
func (c *Crudr) RunRetention(ctx context.Context, cfg RetentionConfig) error {
	if cfg.RetentionDays <= 0 {
		c.logger.Info("ops retention disabled (set OPENAUDIO_MEDIORUM_OPS_RETENTION_DAYS to enable)")
		<-ctx.Done()
		return ctx.Err()
	}
	if cfg.SweepInterval <= 0 {
		return errors.New("retention sweep interval must be positive")
	}
	c.logger.Info("ops retention enabled",
		zap.Int("retention_days", cfg.RetentionDays),
		zap.Duration("sweep_interval", cfg.SweepInterval),
		zap.Int("batch_limit", cfg.SweepBatchLimit),
		zap.Duration("cursor_safety_margin", cfg.CursorSafetyMargin))
	ticker := time.NewTicker(cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := c.retentionTick(ctx, cfg); err != nil {
				c.logger.Warn("retention sweep tick failed", zap.Error(err))
			}
		}
	}
}

// maxBatchesPerTick caps how many delete batches a single retention tick
// will run. The product of maxBatchesPerTick * SweepBatchLimit is the
// upper bound on rows removed per tick — keeps each tick's wall-clock and
// WAL pressure predictable even on a node with a multi-million-row
// backlog after enabling retention. At the default 1h interval, 10
// batches × 10k = up to 100k rows/tick × 24 ticks/day = 2.4M rows/day,
// which exceeds the observed worst-case 1.1M ops/day write rate.
const maxBatchesPerTick = 10

// retentionTick runs a bounded retention sweep. It computes the cutoff
// once per tick, applies the cursor-floor invariant, then loops up to
// maxBatchesPerTick batches so a tick on a backlogged node makes
// measurable progress without monopolizing the DB.
func (c *Crudr) retentionTick(ctx context.Context, cfg RetentionConfig) error {
	cutoff, reason, err := c.computeRetentionCutoff(ctx, cfg)
	if err != nil {
		return err
	}
	if cutoff == "" {
		retention.RetentionSweepsSkipped.Add(1)
		c.logger.Info("retention sweep skipped", zap.String("reason", reason))
		return nil
	}

	batch := cfg.SweepBatchLimit
	if batch <= 0 {
		batch = 10000
	}

	var totalDeleted int64
	for i := 0; i < maxBatchesPerTick; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Use a MATERIALIZED CTE so Postgres evaluates the SELECT once,
		// honoring the LIMIT, and feeds the resulting ulid set to the
		// DELETE. Without MATERIALIZED, Postgres 12+ may inline the CTE
		// and the LIMIT semantics under a "DELETE ... IN (subquery)"
		// shape become harder to reason about; we want the bounded-batch
		// guarantee to hold regardless of planner version.
		res := c.DB.WithContext(ctx).Exec(`
			WITH victims AS MATERIALIZED (
				SELECT ulid
				FROM ops
				WHERE ulid < ?
				ORDER BY ulid ASC
				LIMIT ?
			)
			DELETE FROM ops WHERE ulid IN (SELECT ulid FROM victims)
		`, cutoff, batch)
		if res.Error != nil {
			return fmt.Errorf("retention delete: %w", res.Error)
		}
		totalDeleted += res.RowsAffected
		if res.RowsAffected < int64(batch) {
			break
		}
	}

	if totalDeleted > 0 {
		retention.RetentionOpsDeleted.Add(uint64(totalDeleted))
		c.logger.Info("retention sweep deleted ops",
			zap.Int64("rows_deleted", totalDeleted),
			zap.String("cutoff_ulid", cutoff))
	} else {
		retention.RetentionSweepsSkipped.Add(1)
		c.logger.Debug("retention sweep: no eligible rows",
			zap.String("cutoff_ulid", cutoff))
	}
	return nil
}

// computeRetentionCutoff returns the ULID below which ops are eligible for
// deletion under the given policy. Returns ("", reason, nil) when no rows
// are eligible:
//
//   - any peer cursor is NULL or empty (a peer that has not advanced is
//     treated as the most conservative possible cursor),
//   - the smallest non-empty cursor is older than the age cutoff (a peer
//     is more behind than the retention window allows; keep all ops).
//
// The cutoff itself is min(age_cutoff, cursor_floor_with_margin):
// whichever bound is tighter wins. The cursor floor is load-bearing
// (Topic 7 / cursor-floor invariant): no op younger than the slowest
// reachable peer's cursor may be deleted.
func (c *Crudr) computeRetentionCutoff(ctx context.Context, cfg RetentionConfig) (string, string, error) {
	ageCutoff := time.Now().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)
	ageCutoffULID, err := ulidAtTime(ageCutoff)
	if err != nil {
		return "", "compute age cutoff", err
	}

	var cursors []Cursor
	if err := c.DB.WithContext(ctx).Find(&cursors).Error; err != nil {
		return "", "load cursors", fmt.Errorf("load cursors: %w", err)
	}

	// Self-cursor sentinel: a node may carry a cursor row for itself
	// (rare; only if it was ever asked to sweep itself). Skip it so we
	// don't refuse to retire any op just because the self-row is empty.
	//
	// Empty cursor sentinel: a peer that has never advanced is treated
	// as the most conservative possible cursor — it blocks all deletion.
	// Walk every cursor so the skip reason is stable (the first peer
	// encountered with an empty cursor is reported) regardless of the
	// row order Postgres returns.
	floor := ""
	emptyPeer := ""
	for _, cur := range cursors {
		if cur.Host == c.host {
			continue
		}
		if cur.LastULID == "" {
			if emptyPeer == "" || cur.Host < emptyPeer {
				emptyPeer = cur.Host
			}
			continue
		}
		if floor == "" || cur.LastULID < floor {
			floor = cur.LastULID
		}
	}
	if emptyPeer != "" {
		return "", fmt.Sprintf("empty cursor for peer %s blocks deletion", emptyPeer), nil
	}

	floorWithMargin := floor
	if floor != "" && cfg.CursorSafetyMargin > 0 {
		floorWithMargin, err = ulidShiftBack(floor, cfg.CursorSafetyMargin)
		if err != nil {
			return "", "apply cursor safety margin", err
		}
	}

	cutoff := ageCutoffULID
	if floor != "" && floorWithMargin < cutoff {
		cutoff = floorWithMargin
	}

	// Fast-path: skip the delete if nothing is eligible. This is the
	// common case once retention has caught up — most ticks are no-ops.
	minULID, err := c.MinAvailableULID(ctx)
	if err != nil {
		return "", "load min ulid", err
	}
	if minULID == "" {
		return "", "ops table empty", nil
	}
	if minULID >= cutoff {
		return "", fmt.Sprintf("no rows older than cutoff (min=%s, cutoff=%s)", minULID, cutoff), nil
	}
	return cutoff, "", nil
}

// ulidAtTime returns the smallest ULID with a timestamp >= t. We use all-zero
// entropy so the returned ULID is the lexicographic lower bound for the
// given millisecond — i.e. any op with timestamp < t has a ULID < the
// returned value.
func ulidAtTime(t time.Time) (string, error) {
	id, err := ulid.New(ulid.Timestamp(t), bytes.NewReader(make([]byte, 16)))
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// ulidShiftBack returns a ULID whose timestamp is d earlier than the
// timestamp encoded in srcULID. Entropy bytes are zeroed so the shifted
// ULID is the lower bound of its millisecond.
func ulidShiftBack(srcULID string, d time.Duration) (string, error) {
	id, err := ulid.Parse(srcULID)
	if err != nil {
		return "", fmt.Errorf("parse ulid %q: %w", srcULID, err)
	}
	t := ulid.Time(id.Time()).Add(-d)
	return ulidAtTime(t)
}

// ServeCrudSweep returns the ops and the retention metadata that the sweep
// HTTP handler should advertise. When the caller's after parameter falls
// below this node's min(ulid), the second return is a non-empty string
// containing the smallest ULID currently available locally; callers should
// advertise this on the response (e.g. via the X-Retention-Gap-Min-Ulid
// header) so the requesting peer can advance its cursor explicitly
// rather than silently skipping the gap.
//
// This helper exists so the server-side handler and tests can share the
// gap-detection logic. The handler still applies its own gossip filter on
// the returned op slice.
func (c *Crudr) ServeCrudSweep(ctx context.Context, after string, limit int) (ops []*Op, gapMinULID string, err error) {
	minULID, err := c.MinAvailableULID(ctx)
	if err != nil {
		return nil, "", err
	}
	// The gap signal fires when the caller's cursor is strictly less than
	// the smallest ULID we still have. An empty after is normal first-time
	// sweep behavior, not a gap.
	if after != "" && minULID != "" && after < minULID {
		gapMinULID = minULID
	}

	if err := c.DB.WithContext(ctx).
		Where("ulid > ?", after).
		Limit(limit).
		Order("ulid asc").
		Find(&ops).Error; err != nil {
		return nil, "", err
	}
	return ops, gapMinULID, nil
}

// HTTP headers used by the sweep handler to advertise the retention-gap
// signal and the lowest locally-available ULID. Clients that don't
// understand these headers ignore them and proceed as before; clients
// that do understand them advance their cursor to gapMinULID instead of
// silently skipping the missing range.
const (
	HeaderRetentionGap = "X-Mediorum-Retention-Gap"
	HeaderAvailableMin = "X-Mediorum-Available-Min-Ulid"
)

// MarkSweepGapAdvance increments the gap-advance counter. Exposed for the
// sweep client; tests can read retention.SweepGapAdvances directly.
func MarkSweepGapAdvance() { retention.SweepGapAdvances.Add(1) }

// gapULIDClockSkewWindow is the maximum forward time skew we'll accept on
// an advertised gap ulid. A peer whose advertised ulid decodes to a time
// more than this far in the future is treated as misconfigured or
// hostile, and the gap signal is ignored. 30 minutes is wide enough to
// cover legitimate cross-host clock drift while still preventing a
// forged "far future" ulid from permanently silencing a sweep stream.
const gapULIDClockSkewWindow = 30 * time.Minute

// isValidGapULID returns true when the candidate ulid can be safely used
// to advance the local sweep cursor. It must parse, and its decoded
// timestamp must not be more than gapULIDClockSkewWindow ahead of the
// local wall clock. Without this guard, a hostile peer that emits a
// forged future ulid could permanently silence one of our sweep streams
// by jumping the cursor past every legitimate op.
func isValidGapULID(candidate string) bool {
	id, err := ulid.Parse(candidate)
	if err != nil {
		return false
	}
	return ulid.Time(id.Time()).Before(time.Now().Add(gapULIDClockSkewWindow))
}
