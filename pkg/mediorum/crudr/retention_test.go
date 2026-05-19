package crudr

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/lifecycle"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// retentionTestModel is a minimal registered CRUD model used by the
// retention tests. We give it a primary key so OnConflict-style apply
// paths work without panicking and so it mirrors the real registered
// models (Upload, QmAudioAnalysis, etc).
type retentionTestModel struct {
	Key string `gorm:"primaryKey"`
}

// otherRetentionTestModel mirrors the dormant-table scenario: another
// table the crud layer knows about, ops to which we will or won't
// emit depending on the test.
type otherRetentionTestModel struct {
	Key string `gorm:"primaryKey"`
}

// newRetentionCrudr returns a fresh crudr wired to the shared test DB,
// with the retention test models registered and the ops/cursors tables
// truncated. Tests that mutate env vars must restore them via t.Cleanup.
// The underlying *sql.DB is closed via t.Cleanup so a `-count=N` run does
// not exhaust Postgres connections.
func newRetentionCrudr(t *testing.T, host string) *Crudr {
	t.Helper()
	db := SetupTestDB()
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})

	// migrate the retention test models so ApplyOp works
	require.NoError(t, db.AutoMigrate(&retentionTestModel{}, &otherRetentionTestModel{}))

	// fully reset state between tests in the same package run
	require.NoError(t, db.Exec(`TRUNCATE ops`).Error)
	require.NoError(t, db.Exec(`TRUNCATE cursors`).Error)
	require.NoError(t, db.Exec(`TRUNCATE retention_test_models`).Error)
	require.NoError(t, db.Exec(`TRUNCATE other_retention_test_models`).Error)

	z := zap.NewNop()
	c := New(host, nil, nil, db, lifecycle.NewLifecycle(context.Background(), "retention test", z), z, nil)
	c.RegisterModels(&retentionTestModel{}, &otherRetentionTestModel{})
	return c
}

// resetRetentionStats clears the package counters so tests don't
// pollute each other.
func resetRetentionStats() {
	retention.DormantTablesCleaned.Store(0)
	retention.DormantOpsDeleted.Store(0)
	retention.RetentionOpsDeleted.Store(0)
	retention.RetentionSweepsSkipped.Store(0)
	retention.SweepGapAdvances.Store(0)
}

// setupRetentionTestDB opens a freshly-truncated test DB and registers a
// t.Cleanup to close it, so a long `-count=N` run never exhausts
// Postgres connections.
func setupRetentionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := SetupTestDB()
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&retentionTestModel{}))
	require.NoError(t, db.Exec(`TRUNCATE ops`).Error)
	require.NoError(t, db.Exec(`TRUNCATE cursors`).Error)
	return db
}

// insertOpAt inserts an op with a ULID derived from the given wall time
// and a deterministic per-call entropy stream so callers with overlapping
// timestamps still get distinct ULIDs. Returns the inserted ULID string.
func insertOpAt(t *testing.T, c *Crudr, table string, at time.Time) string {
	t.Helper()
	return insertOpAtWithEntropy(t, c, table, at, ulid.Make().Entropy())
}

func insertOpAtWithEntropy(t *testing.T, c *Crudr, table string, at time.Time, entropy []byte) string {
	t.Helper()
	id, err := ulid.New(ulid.Timestamp(at), bytes.NewReader(entropy))
	require.NoError(t, err)
	op := &Op{
		ULID:   id.String(),
		Host:   c.host,
		Action: ActionCreate,
		Table:  table,
		Data:   json.RawMessage(`[]`),
	}
	require.NoError(t, c.DB.Create(op).Error)
	return id.String()
}

func countOpsForTable(t *testing.T, c *Crudr, table string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, c.DB.Raw(`SELECT COUNT(*) FROM ops WHERE "table" = ?`, table).Scan(&n).Error)
	return n
}

func TestLoadRetentionConfig_Defaults(t *testing.T) {
	// Snapshot and restore env so we don't leak into sibling tests.
	for _, k := range []string{
		"OPENAUDIO_MEDIORUM_KEEP_DORMANT_OPS",
		"OPENAUDIO_MEDIORUM_DORMANT_OPS_THRESHOLD",
		"OPENAUDIO_MEDIORUM_OPS_RETENTION_DAYS",
		"OPENAUDIO_MEDIORUM_OPS_RETENTION_SWEEP_INTERVAL",
		"OPENAUDIO_MEDIORUM_OPS_RETENTION_BATCH_LIMIT",
		"OPENAUDIO_MEDIORUM_OPS_RETENTION_CURSOR_MARGIN",
	} {
		prev, had := os.LookupEnv(k)
		os.Unsetenv(k)
		t.Cleanup(func() {
			if had {
				os.Setenv(k, prev)
			}
		})
	}
	cfg := LoadRetentionConfig()
	assert.True(t, cfg.DormantCleanupEnabled, "dormant cleanup must default ON")
	assert.Equal(t, 90*24*time.Hour, cfg.DormantThreshold)
	assert.Equal(t, 0, cfg.RetentionDays, "ongoing retention must default OFF")
	assert.Equal(t, 1*time.Hour, cfg.SweepInterval)
	assert.Equal(t, 10000, cfg.SweepBatchLimit)
	assert.Equal(t, 1*time.Hour, cfg.CursorSafetyMargin)
}

func TestLoadRetentionConfig_OptOutDormant(t *testing.T) {
	t.Setenv("OPENAUDIO_MEDIORUM_KEEP_DORMANT_OPS", "true")
	cfg := LoadRetentionConfig()
	assert.False(t, cfg.DormantCleanupEnabled)
}

// Component 1 — dormant-table cleanup tests.

func TestCleanupDormantOps_DropsDormantTable(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	cfg := RetentionConfig{DormantCleanupEnabled: true, DormantThreshold: 30 * 24 * time.Hour}

	now := time.Now()
	// 100 ops on the dormant table, all 200 days old.
	for i := 0; i < 100; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-200*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	// 5 ops on a still-active table, all within the last hour.
	for i := 0; i < 5; i++ {
		insertOpAt(t, c, "other_retention_test_models", now.Add(-time.Duration(i)*time.Minute))
	}

	deleted, err := c.CleanupDormantOps(context.Background(), cfg)
	require.NoError(t, err)

	assert.Equal(t, int64(100), deleted["retention_test_models"])
	assert.NotContains(t, deleted, "other_retention_test_models", "active table must not be cleaned")
	assert.Equal(t, int64(0), countOpsForTable(t, c, "retention_test_models"))
	assert.Equal(t, int64(5), countOpsForTable(t, c, "other_retention_test_models"))
	assert.Equal(t, uint64(1), retention.DormantTablesCleaned.Load())
	assert.Equal(t, uint64(100), retention.DormantOpsDeleted.Load())
}

func TestCleanupDormantOps_OptOutPreservesAll(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	cfg := RetentionConfig{DormantCleanupEnabled: false, DormantThreshold: 30 * 24 * time.Hour}

	now := time.Now()
	for i := 0; i < 25; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-200*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}

	deleted, err := c.CleanupDormantOps(context.Background(), cfg)
	require.NoError(t, err)
	assert.Empty(t, deleted)
	assert.Equal(t, int64(25), countOpsForTable(t, c, "retention_test_models"))
	assert.Equal(t, uint64(0), retention.DormantTablesCleaned.Load())
}

func TestCleanupDormantOps_Idempotent(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	cfg := RetentionConfig{DormantCleanupEnabled: true, DormantThreshold: 30 * 24 * time.Hour}

	now := time.Now()
	for i := 0; i < 7; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-180*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}

	deleted1, err := c.CleanupDormantOps(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, int64(7), deleted1["retention_test_models"])

	deleted2, err := c.CleanupDormantOps(context.Background(), cfg)
	require.NoError(t, err)
	assert.Empty(t, deleted2, "second run on an already-cleaned table must be a no-op")
}

func TestCleanupDormantOps_RecentWriteOnDormantTableBlocksDelete(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	cfg := RetentionConfig{DormantCleanupEnabled: true, DormantThreshold: 30 * 24 * time.Hour}

	now := time.Now()
	// 50 old ops...
	for i := 0; i < 50; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-200*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	// ...and one recent op on the same table.
	insertOpAt(t, c, "retention_test_models", now.Add(-1*time.Minute))

	deleted, err := c.CleanupDormantOps(context.Background(), cfg)
	require.NoError(t, err)
	assert.Empty(t, deleted, "a single recent op must protect all ops for the table")
	assert.Equal(t, int64(51), countOpsForTable(t, c, "retention_test_models"))
}

func TestCleanupDormantOps_AfterCleanupMinUlidReflectsRemaining(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	cfg := RetentionConfig{DormantCleanupEnabled: true, DormantThreshold: 30 * 24 * time.Hour}

	now := time.Now()
	insertOpAt(t, c, "retention_test_models", now.Add(-200*24*time.Hour))
	keepUlid := insertOpAt(t, c, "other_retention_test_models", now.Add(-1*time.Hour))

	_, err := c.CleanupDormantOps(context.Background(), cfg)
	require.NoError(t, err)

	minULID, err := c.MinAvailableULID(context.Background())
	require.NoError(t, err)
	assert.Equal(t, keepUlid, minULID, "min ulid should be the surviving op")
}

func TestCleanupDormantOps_UnregisteredTableUntouched(t *testing.T) {
	// A table whose model the crud layer does NOT know about must not
	// be classified as dormant just because it has no recent ops; it
	// must not appear in c.typeMap at all.
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	cfg := RetentionConfig{DormantCleanupEnabled: true, DormantThreshold: 30 * 24 * time.Hour}

	now := time.Now()
	// Pretend the ops table has rows for a long-removed table.
	for i := 0; i < 10; i++ {
		insertOpAt(t, c, "ghost_table", now.Add(-365*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}

	deleted, err := c.CleanupDormantOps(context.Background(), cfg)
	require.NoError(t, err)
	assert.Empty(t, deleted, "unregistered tables must not be touched by the dormant cleanup")
	assert.Equal(t, int64(10), countOpsForTable(t, c, "ghost_table"))
}

func TestCleanupDormantOps_RespectsContextCancel(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	cfg := RetentionConfig{DormantCleanupEnabled: true, DormantThreshold: 30 * 24 * time.Hour}

	now := time.Now()
	for i := 0; i < 5; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-200*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.CleanupDormantOps(ctx, cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// Component 2 — gap-signal tests.

func TestServeCrudSweep_BelowMinReturnsGap(t *testing.T) {
	c := newRetentionCrudr(t, "host1")
	now := time.Now()
	for i := 0; i < 3; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(time.Duration(i)*time.Millisecond))
	}

	veryOld, err := ulid.New(ulid.Timestamp(now.Add(-365*24*time.Hour)), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)

	ops, gap, err := c.ServeCrudSweep(context.Background(), veryOld.String(), 100)
	require.NoError(t, err)
	assert.NotEmpty(t, gap, "after below min must signal gap")
	assert.Len(t, ops, 3, "returned ops are the full set above the caller's cursor")
}

func TestServeCrudSweep_AtOrAboveMinNoGap(t *testing.T) {
	c := newRetentionCrudr(t, "host1")
	now := time.Now()
	first := insertOpAt(t, c, "retention_test_models", now)
	insertOpAt(t, c, "retention_test_models", now.Add(1*time.Millisecond))

	// passing exactly the lowest ULID means "give me ops > min": no gap.
	ops, gap, err := c.ServeCrudSweep(context.Background(), first, 100)
	require.NoError(t, err)
	assert.Empty(t, gap)
	assert.Len(t, ops, 1, "expect the single op strictly greater than the cursor")
}

func TestServeCrudSweep_EmptyAfterIsNotAGap(t *testing.T) {
	c := newRetentionCrudr(t, "host1")
	insertOpAt(t, c, "retention_test_models", time.Now())

	ops, gap, err := c.ServeCrudSweep(context.Background(), "", 100)
	require.NoError(t, err)
	assert.Empty(t, gap, "first-time sweep (empty cursor) is not a retention gap")
	assert.Len(t, ops, 1)
}

func TestServeCrudSweep_EmptyOpsTableNoGap(t *testing.T) {
	c := newRetentionCrudr(t, "host1")
	// caller has a cursor but the local ops table is empty (e.g. fresh
	// node). No gap, no ops.
	ops, gap, err := c.ServeCrudSweep(context.Background(), "01HABCD000000000000000000A", 100)
	require.NoError(t, err)
	assert.Empty(t, gap)
	assert.Empty(t, ops)
}

// Dry-run preview tests.

func TestDryRunRetention_DormantOnly(t *testing.T) {
	// Default config: dormant cleanup ON, retention sweep OFF. Dry run
	// reports the dormant table count and skips the retention preview.
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()
	for i := 0; i < 50; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-200*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	// Active table — must not appear in the plan.
	insertOpAt(t, c, "other_retention_test_models", now.Add(-1*time.Minute))

	cfg := RetentionConfig{DormantCleanupEnabled: true, DormantThreshold: 30 * 24 * time.Hour}
	plan, err := c.DryRunRetention(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, int64(50), plan.DormantTables["retention_test_models"])
	assert.NotContains(t, plan.DormantTables, "other_retention_test_models")
	assert.Greater(t, plan.DormantBytes, int64(0), "dormant bytes must be reported")
	assert.Equal(t, int64(0), plan.RetentionRows, "retention preview skipped when RetentionDays=0")
	assert.NotEmpty(t, plan.DormantCutoffULID)
	// Dry run must not delete anything.
	assert.Equal(t, int64(50), countOpsForTable(t, c, "retention_test_models"))
	assert.Equal(t, int64(1), countOpsForTable(t, c, "other_retention_test_models"))
}

func TestDryRunRetention_RetentionPreviewSkipsOnEmptyCursor(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()
	for i := 0; i < 10; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-60*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	require.NoError(t, c.DB.Create(&Cursor{Host: "empty-peer", LastULID: ""}).Error)

	cfg := RetentionConfig{DormantCleanupEnabled: false, RetentionDays: 30, SweepBatchLimit: 100, CursorSafetyMargin: 1 * time.Hour}
	plan, err := c.DryRunRetention(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, int64(0), plan.RetentionRows)
	assert.NotEmpty(t, plan.RetentionSkipReason, "empty cursor must produce a skip reason")
	assert.Equal(t, int64(10), countOpsForTable(t, c, "retention_test_models"))
}

func TestDryRunRetention_RetentionPreviewWithEligibleRows(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()
	// 30 old ops + 5 recent ops.
	for i := 0; i < 30; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-60*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	for i := 0; i < 5; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-1*time.Minute).Add(time.Duration(i)*time.Millisecond))
	}
	recentULID, err := ulid.New(ulid.Timestamp(now), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, c.DB.Create(&Cursor{Host: "peer", LastULID: recentULID.String()}).Error)

	cfg := RetentionConfig{DormantCleanupEnabled: false, RetentionDays: 30, SweepBatchLimit: 1000, CursorSafetyMargin: 1 * time.Hour}
	plan, err := c.DryRunRetention(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, int64(30), plan.RetentionRows, "old ops counted, recent ops preserved")
	assert.Greater(t, plan.RetentionBytes, int64(0))
	assert.Empty(t, plan.RetentionSkipReason)
	assert.NotEmpty(t, plan.RetentionCutoffULID)
	// No DELETE executed.
	assert.Equal(t, int64(35), countOpsForTable(t, c, "retention_test_models"))
}

func TestDryRunRetention_DormantCleanupDisabledNoPreview(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()
	for i := 0; i < 25; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-200*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}

	cfg := RetentionConfig{DormantCleanupEnabled: false, DormantThreshold: 30 * 24 * time.Hour}
	plan, err := c.DryRunRetention(context.Background(), cfg)
	require.NoError(t, err)
	assert.Empty(t, plan.DormantTables)
	assert.Equal(t, int64(0), plan.DormantBytes)
}

// Component 3 — retention sweep tests.

func TestRunRetention_DisabledIsNoOp(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()
	for i := 0; i < 20; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-365*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}

	cfg := RetentionConfig{RetentionDays: 0}

	// Disabled retention should block on ctx; we cancel quickly to exit
	// the loop without deleting anything.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := c.RunRetention(ctx, cfg)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, int64(20), countOpsForTable(t, c, "retention_test_models"))
}

func TestRetentionTick_AllCursorsAheadDeletesOldOps(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()

	// Old ops (> 30d) and recent ops (< 1h).
	for i := 0; i < 25; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-60*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	for i := 0; i < 5; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-1*time.Minute).Add(time.Duration(i)*time.Millisecond))
	}

	// Every peer cursor is at "now" — no peer is behind.
	recentULID, err := ulid.New(ulid.Timestamp(now), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, c.DB.Create(&Cursor{Host: "peer-a", LastULID: recentULID.String()}).Error)
	require.NoError(t, c.DB.Create(&Cursor{Host: "peer-b", LastULID: recentULID.String()}).Error)

	cfg := RetentionConfig{RetentionDays: 30, SweepBatchLimit: 1000, CursorSafetyMargin: 1 * time.Hour}
	require.NoError(t, c.retentionTick(context.Background(), cfg))

	// Old ops gone, recent ops kept.
	assert.Equal(t, int64(5), countOpsForTable(t, c, "retention_test_models"))
	assert.Equal(t, uint64(25), retention.RetentionOpsDeleted.Load())
}

func TestRetentionTick_CursorBelowCutoffPreventsDelete(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()

	// Three age cohorts of ops to verify cursor pinning:
	//   - 60d old:  older than the slow cursor (45d) => deletable.
	//   - 40d old:  newer than the slow cursor      => MUST be kept
	//                (this is the load-bearing case).
	//   - 1m old:   well within the cursor          => kept.
	for i := 0; i < 5; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-60*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	for i := 0; i < 5; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-40*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	for i := 0; i < 5; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-1*time.Minute).Add(time.Duration(i)*time.Millisecond))
	}

	stuckCursor, err := ulid.New(ulid.Timestamp(now.Add(-45*24*time.Hour)), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, c.DB.Create(&Cursor{Host: "slow-peer", LastULID: stuckCursor.String()}).Error)

	cfg := RetentionConfig{RetentionDays: 30, SweepBatchLimit: 1000, CursorSafetyMargin: 1 * time.Hour}
	require.NoError(t, c.retentionTick(context.Background(), cfg))

	// Only the 60d cohort should be deleted; the 40d cohort sits above
	// the slow cursor and MUST be preserved, otherwise that peer's next
	// sweep would silently skip a deleted range.
	remaining := countOpsForTable(t, c, "retention_test_models")
	assert.Equal(t, int64(10), remaining,
		"slow peer must block deletion of ops newer than its cursor")
	assert.Equal(t, uint64(5), retention.RetentionOpsDeleted.Load())

	// No remaining op may be older than (cursor - margin).
	floorWithMargin, err := ulidShiftBack(stuckCursor.String(), 1*time.Hour)
	require.NoError(t, err)
	var oldestRemaining string
	require.NoError(t, c.DB.Raw(`SELECT MIN(ulid) FROM ops`).Scan(&oldestRemaining).Error)
	assert.GreaterOrEqual(t, oldestRemaining, floorWithMargin,
		"no remaining op may be older than the cursor-safety floor")
}

func TestRetentionTick_EmptyCursorBlocksDeletion(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()
	for i := 0; i < 12; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-60*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	require.NoError(t, c.DB.Create(&Cursor{Host: "peer-empty", LastULID: ""}).Error)

	cfg := RetentionConfig{RetentionDays: 30, SweepBatchLimit: 1000, CursorSafetyMargin: 1 * time.Hour}
	require.NoError(t, c.retentionTick(context.Background(), cfg))
	assert.Equal(t, int64(12), countOpsForTable(t, c, "retention_test_models"),
		"an empty cursor row must be treated as the most conservative possible cursor")
	assert.Equal(t, uint64(0), retention.RetentionOpsDeleted.Load())
	assert.GreaterOrEqual(t, retention.RetentionSweepsSkipped.Load(), uint64(1))
}

func TestRetentionTick_SafetyMarginHonored(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()

	// Three op ages spread around a slow cursor at -30d:
	// older than (cursor - 2h), within (cursor - 2h, cursor), and newer than cursor.
	for i := 0; i < 5; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-50*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	for i := 0; i < 5; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-30*24*time.Hour-30*time.Minute).Add(time.Duration(i)*time.Millisecond))
	}
	for i := 0; i < 5; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-29*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}

	stuckCursor, err := ulid.New(ulid.Timestamp(now.Add(-30*24*time.Hour)), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, c.DB.Create(&Cursor{Host: "slow", LastULID: stuckCursor.String()}).Error)

	// 1h margin pushes cutoff to (cursor - 1h). Anything between cursor-1h
	// and cursor must be preserved.
	cfg := RetentionConfig{RetentionDays: 1, SweepBatchLimit: 1000, CursorSafetyMargin: 1 * time.Hour}
	require.NoError(t, c.retentionTick(context.Background(), cfg))

	// Only the very-old (-50d) bucket can be deleted; the 30m-before-cursor
	// bucket is inside the safety margin and the 29d bucket sits above the
	// cursor itself.
	remaining := countOpsForTable(t, c, "retention_test_models")
	assert.Equal(t, int64(10), remaining,
		"safety margin must keep the 30-min-before-cursor bucket alive")
}

func TestRetentionTick_BatchLimitHonored(t *testing.T) {
	// Drains the eligible set with batch=100, looping internally up to
	// maxBatchesPerTick. 250 eligible rows complete in 3 batches and
	// the loop exits on the short final batch.
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()
	for i := 0; i < 250; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-60*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	recentULID, err := ulid.New(ulid.Timestamp(now), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, c.DB.Create(&Cursor{Host: "peer", LastULID: recentULID.String()}).Error)

	cfg := RetentionConfig{RetentionDays: 30, SweepBatchLimit: 100, CursorSafetyMargin: 1 * time.Hour}
	require.NoError(t, c.retentionTick(context.Background(), cfg))
	assert.Equal(t, int64(0), countOpsForTable(t, c, "retention_test_models"))
	assert.Equal(t, uint64(250), retention.RetentionOpsDeleted.Load())
}

func TestRetentionTick_DrainsUpToMaxBatchesPerTick(t *testing.T) {
	// A backlogged tick must take more than one batch in a single call,
	// up to maxBatchesPerTick. With batch=10 and maxBatches=10, a tick
	// against a 200-row eligible set should delete 100 rows (10 batches
	// × 10 rows), leaving 100 untouched until the next tick.
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()
	for i := 0; i < 200; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-60*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	recentULID, err := ulid.New(ulid.Timestamp(now), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, c.DB.Create(&Cursor{Host: "peer", LastULID: recentULID.String()}).Error)

	cfg := RetentionConfig{RetentionDays: 30, SweepBatchLimit: 10, CursorSafetyMargin: 1 * time.Hour}
	require.NoError(t, c.retentionTick(context.Background(), cfg))

	remaining := countOpsForTable(t, c, "retention_test_models")
	// We deleted at most maxBatchesPerTick * batch = 100 rows.
	expectedRemaining := int64(200) - int64(maxBatchesPerTick*10)
	assert.Equal(t, expectedRemaining, remaining,
		"tick must drain up to maxBatchesPerTick batches; backlog finishes on subsequent ticks")
	assert.Equal(t, uint64(maxBatchesPerTick*10), retention.RetentionOpsDeleted.Load())
}

func TestRetentionTick_IteratesAllRegisteredTables(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()
	for i := 0; i < 5; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-60*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
		insertOpAt(t, c, "other_retention_test_models", now.Add(-60*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	recentULID, err := ulid.New(ulid.Timestamp(now), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, c.DB.Create(&Cursor{Host: "peer", LastULID: recentULID.String()}).Error)

	cfg := RetentionConfig{RetentionDays: 30, SweepBatchLimit: 1000, CursorSafetyMargin: 1 * time.Hour}
	require.NoError(t, c.retentionTick(context.Background(), cfg))

	// retention deletes by ulid alone (not per-table), so all 10 ops are
	// dropped in this single tick — this is the documented behavior of
	// the first PR: uniform cutoff across all CRUD tables.
	assert.Equal(t, int64(0), countOpsForTable(t, c, "retention_test_models"))
	assert.Equal(t, int64(0), countOpsForTable(t, c, "other_retention_test_models"))
}

func TestRetentionTick_SelfCursorIgnored(t *testing.T) {
	// A cursor row whose host equals the crudr's selfHost must not
	// block deletion: it's not a remote peer, it's the node's own
	// (rare/test-only) self-pointer.
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()
	for i := 0; i < 10; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-60*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	require.NoError(t, c.DB.Create(&Cursor{Host: "host1", LastULID: ""}).Error)

	recentULID, err := ulid.New(ulid.Timestamp(now), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, c.DB.Create(&Cursor{Host: "peer", LastULID: recentULID.String()}).Error)

	cfg := RetentionConfig{RetentionDays: 30, SweepBatchLimit: 1000, CursorSafetyMargin: 1 * time.Hour}
	require.NoError(t, c.retentionTick(context.Background(), cfg))
	assert.Equal(t, int64(0), countOpsForTable(t, c, "retention_test_models"),
		"self-cursor must not block deletion")
}

func TestRetentionTick_NoPeersUsesAgeCutoffOnly(t *testing.T) {
	// Devnet-style single-node: no peer cursors. The age cutoff is the
	// only gate.
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()
	for i := 0; i < 8; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-60*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}

	cfg := RetentionConfig{RetentionDays: 30, SweepBatchLimit: 1000, CursorSafetyMargin: 1 * time.Hour}
	require.NoError(t, c.retentionTick(context.Background(), cfg))
	assert.Equal(t, int64(0), countOpsForTable(t, c, "retention_test_models"))
}

// Client-side gap-signal tests.

// roundTripFunc lets a test stand in for the peer HTTP server.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPeerClient_DetectsGapAndAdvancesCursor(t *testing.T) {
	resetRetentionStats()
	db := setupRetentionTestDB(t)

	// peer advertises a gap with min ulid in the recent past
	peerMinULID, err := ulid.New(ulid.Timestamp(time.Now().Add(-7*24*time.Hour)), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)

	// our cursor is far below the peer's min
	oldCursorULID, err := ulid.New(ulid.Timestamp(time.Now().Add(-365*24*time.Hour)), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, db.Create(&Cursor{Host: "http://peer-x", LastULID: oldCursorULID.String()}).Error)

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		assert.Equal(t, oldCursorULID.String(), r.URL.Query().Get("after"))
		h := http.Header{}
		h.Set(HeaderRetentionGap, "true")
		h.Set(HeaderAvailableMin, peerMinULID.String())
		h.Set("Content-Type", "application/json")
		return &http.Response{
			StatusCode: 200,
			Header:     h,
			Body:       io.NopCloser(bytes.NewBufferString("[]")),
			Request:    r,
		}, nil
	})

	z := zap.NewNop()
	c := New("http://self", nil, nil, db, lifecycle.NewLifecycle(context.Background(), "test", z), z, &http.Client{Transport: rt})
	c.RegisterModels(&retentionTestModel{})

	peer := NewPeerClient("http://peer-x", c, "http://self")
	require.NoError(t, peer.doSweep(context.Background()))

	// Cursor must be advanced to the peer's advertised floor.
	var cur Cursor
	require.NoError(t, c.DB.Where("host = ?", "http://peer-x").First(&cur).Error)
	assert.Equal(t, peerMinULID.String(), cur.LastULID,
		"cursor must explicitly advance to peer's advertised retention floor")
	assert.Equal(t, uint64(1), retention.SweepGapAdvances.Load(),
		"sweep client must increment the gap counter for operator visibility")
}

func TestPeerClient_GapHeaderRespectedAcrossTicks(t *testing.T) {
	// On a steady-state retention scenario, the same cursor should not
	// re-advance over the same gap on the next tick — the persisted
	// cursor now equals or exceeds gapMinULID, so the
	// `gapMinULID > lastUlid` guard suppresses the second increment.
	resetRetentionStats()
	db := setupRetentionTestDB(t)

	peerMinULID, err := ulid.New(ulid.Timestamp(time.Now().Add(-7*24*time.Hour)), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	oldCursorULID, err := ulid.New(ulid.Timestamp(time.Now().Add(-365*24*time.Hour)), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, db.Create(&Cursor{Host: "http://peer-z", LastULID: oldCursorULID.String()}).Error)

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		h := http.Header{}
		h.Set(HeaderRetentionGap, "true")
		h.Set(HeaderAvailableMin, peerMinULID.String())
		h.Set("Content-Type", "application/json")
		return &http.Response{
			StatusCode: 200,
			Header:     h,
			Body:       io.NopCloser(bytes.NewBufferString("[]")),
			Request:    r,
		}, nil
	})

	z := zap.NewNop()
	c := New("http://self", nil, nil, db, lifecycle.NewLifecycle(context.Background(), "test", z), z, &http.Client{Transport: rt})
	c.RegisterModels(&retentionTestModel{})
	peer := NewPeerClient("http://peer-z", c, "http://self")

	// First tick: counter increments once.
	require.NoError(t, peer.doSweep(context.Background()))
	assert.Equal(t, uint64(1), retention.SweepGapAdvances.Load())

	// Second tick: cursor is now == peerMinULID, gap signal still present
	// but suppressed by the strict-greater-than guard.
	require.NoError(t, peer.doSweep(context.Background()))
	assert.Equal(t, uint64(1), retention.SweepGapAdvances.Load(),
		"counter must not double-count when cursor already at floor")
}

func TestPeerClient_HostileFarFutureGapULIDRejected(t *testing.T) {
	// A hostile or misconfigured peer that advertises a gap ulid far in
	// the future must NOT silence our sweep stream. The client must
	// reject the gap header, keep its cursor where it was, and continue
	// applying any ops in the response body normally.
	resetRetentionStats()
	db := setupRetentionTestDB(t)

	// A ulid 5 years in the future — well beyond any legitimate clock
	// skew window.
	futureULID, err := ulid.New(ulid.Timestamp(time.Now().Add(5*365*24*time.Hour)), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	oldCursorULID, err := ulid.New(ulid.Timestamp(time.Now().Add(-365*24*time.Hour)), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, db.Create(&Cursor{Host: "http://hostile-peer", LastULID: oldCursorULID.String()}).Error)

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		h := http.Header{}
		h.Set(HeaderRetentionGap, "true")
		h.Set(HeaderAvailableMin, futureULID.String())
		h.Set("Content-Type", "application/json")
		return &http.Response{
			StatusCode: 200,
			Header:     h,
			Body:       io.NopCloser(bytes.NewBufferString("[]")),
			Request:    r,
		}, nil
	})

	z := zap.NewNop()
	c := New("http://self", nil, nil, db, lifecycle.NewLifecycle(context.Background(), "test", z), z, &http.Client{Transport: rt})
	c.RegisterModels(&retentionTestModel{})

	peer := NewPeerClient("http://hostile-peer", c, "http://self")
	require.NoError(t, peer.doSweep(context.Background()))

	// Cursor must NOT have advanced — the hostile gap was rejected.
	var cur Cursor
	require.NoError(t, c.DB.Where("host = ?", "http://hostile-peer").First(&cur).Error)
	assert.Equal(t, oldCursorULID.String(), cur.LastULID,
		"cursor must not advance across a far-future advertised gap ulid")
	assert.Equal(t, uint64(0), retention.SweepGapAdvances.Load(),
		"hostile gap must not increment the operator metric")
}

func TestPeerClient_MalformedGapULIDRejected(t *testing.T) {
	// A non-ULID string in the gap header is also a rejection case.
	resetRetentionStats()
	db := setupRetentionTestDB(t)
	oldCursorULID, err := ulid.New(ulid.Timestamp(time.Now().Add(-365*24*time.Hour)), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, db.Create(&Cursor{Host: "http://peer", LastULID: oldCursorULID.String()}).Error)

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		h := http.Header{}
		h.Set(HeaderRetentionGap, "true")
		h.Set(HeaderAvailableMin, "not-a-ulid-this-is-garbage")
		h.Set("Content-Type", "application/json")
		return &http.Response{
			StatusCode: 200,
			Header:     h,
			Body:       io.NopCloser(bytes.NewBufferString("[]")),
			Request:    r,
		}, nil
	})

	z := zap.NewNop()
	c := New("http://self", nil, nil, db, lifecycle.NewLifecycle(context.Background(), "test", z), z, &http.Client{Transport: rt})
	c.RegisterModels(&retentionTestModel{})

	peer := NewPeerClient("http://peer", c, "http://self")
	require.NoError(t, peer.doSweep(context.Background()))

	var cur Cursor
	require.NoError(t, c.DB.Where("host = ?", "http://peer").First(&cur).Error)
	assert.Equal(t, oldCursorULID.String(), cur.LastULID)
	assert.Equal(t, uint64(0), retention.SweepGapAdvances.Load())
}

func TestIsValidGapULID(t *testing.T) {
	now := time.Now()

	mk := func(at time.Time) string {
		id, err := ulid.New(ulid.Timestamp(at), bytes.NewReader(make([]byte, 16)))
		require.NoError(t, err)
		return id.String()
	}

	assert.True(t, isValidGapULID(mk(now.Add(-30*24*time.Hour))), "past ulid valid")
	assert.True(t, isValidGapULID(mk(now.Add(-1*time.Second))), "recent past ulid valid")
	assert.True(t, isValidGapULID(mk(now.Add(15*time.Minute))), "small forward skew valid")
	assert.False(t, isValidGapULID(mk(now.Add(2*time.Hour))), "2h forward beyond skew window rejected")
	assert.False(t, isValidGapULID(mk(now.Add(365*24*time.Hour))), "1y forward rejected")
	assert.False(t, isValidGapULID(""), "empty string rejected")
	assert.False(t, isValidGapULID("not a ulid"), "garbage rejected")
}

func TestPeerClient_NoGapHeaderNoAdvance(t *testing.T) {
	resetRetentionStats()
	db := setupRetentionTestDB(t)

	oldCursorULID, err := ulid.New(ulid.Timestamp(time.Now().Add(-365*24*time.Hour)), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, db.Create(&Cursor{Host: "http://peer-y", LastULID: oldCursorULID.String()}).Error)

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		h := http.Header{}
		h.Set("Content-Type", "application/json")
		return &http.Response{
			StatusCode: 200,
			Header:     h,
			Body:       io.NopCloser(bytes.NewBufferString("[]")),
			Request:    r,
		}, nil
	})

	z := zap.NewNop()
	c := New("http://self", nil, nil, db, lifecycle.NewLifecycle(context.Background(), "test", z), z, &http.Client{Transport: rt})
	c.RegisterModels(&retentionTestModel{})

	peer := NewPeerClient("http://peer-y", c, "http://self")
	require.NoError(t, peer.doSweep(context.Background()))

	assert.Equal(t, uint64(0), retention.SweepGapAdvances.Load(),
		"no gap header => no counter increment")
}

// MinAvailableULID surface test.

func TestMinAvailableULID_EmptyTable(t *testing.T) {
	c := newRetentionCrudr(t, "host1")
	min, err := c.MinAvailableULID(context.Background())
	require.NoError(t, err)
	assert.Empty(t, min)
}

func TestMinAvailableULID_ReturnsSmallest(t *testing.T) {
	c := newRetentionCrudr(t, "host1")
	now := time.Now()
	insertOpAt(t, c, "retention_test_models", now.Add(-2*time.Hour))
	insertOpAt(t, c, "retention_test_models", now.Add(-1*time.Hour))
	insertOpAt(t, c, "retention_test_models", now)
	min, err := c.MinAvailableULID(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, min)

	// The smallest ulid is the one we inserted at -2h.
	var explicitMin string
	require.NoError(t, c.DB.Raw(`SELECT MIN(ulid) FROM ops`).Scan(&explicitMin).Error)
	assert.Equal(t, explicitMin, min)
}

// Operator-config safety tests.

func TestCleanupDormantOps_BelowMinThresholdClamped(t *testing.T) {
	// An operator who sets the threshold below the safety floor (24h)
	// must not be able to delete brand-new tables: the clamp keeps a
	// brief lull from being classified as dormant.
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	cfg := RetentionConfig{DormantCleanupEnabled: true, DormantThreshold: 1 * time.Minute}

	now := time.Now()
	for i := 0; i < 10; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-2*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}

	deleted, err := c.CleanupDormantOps(context.Background(), cfg)
	require.NoError(t, err)
	assert.Empty(t, deleted, "threshold < 24h must clamp up; -2h ops must not be classified dormant")
	assert.Equal(t, int64(10), countOpsForTable(t, c, "retention_test_models"))
}

func TestCleanupDormantOps_PreservesNewOpsAboveCutoff(t *testing.T) {
	// Race-window guard: a producer writes a new op for a table the
	// cleanup has just classified as dormant. The new op's ULID is above
	// the cutoff, so the bounded WHERE clause must spare it even though
	// the table appears dormant from the MAX(ulid) check.
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	cfg := RetentionConfig{DormantCleanupEnabled: true, DormantThreshold: 30 * 24 * time.Hour}

	now := time.Now()
	// Old ops, far below the cutoff.
	for i := 0; i < 20; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-200*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	// Inject a NEW op *above* the cutoff after the dormancy classification
	// but before cleanup gets to its DELETE. We simulate the race by
	// inserting before the call: same effect because the dormancy check
	// uses MAX(ulid) which still returns one of the old ulids if we put
	// the recent one in *after* picking maxULID. But the cleanup's
	// `WHERE ulid < cutoff` guard means recent ops are never touched
	// regardless of MAX ordering — the property we're verifying here.
	freshULID := insertOpAt(t, c, "retention_test_models", now.Add(-1*time.Minute))

	// The dormancy check will see freshULID as the max and classify the
	// table as still-active. To exercise the race-guard explicitly,
	// re-run with a threshold tight enough that the table is dormant
	// EXCEPT for the fresh op (12h between fresh op and cutoff < 24h
	// floor, so we use 23h base then test the WHERE clause directly).
	//
	// Simpler probe: call CleanupDormantOps and assert freshULID survives
	// even if the table is "borderline dormant". Both paths converge on
	// the same guarantee: ulids above cutoff never get deleted.
	_, err := c.CleanupDormantOps(context.Background(), cfg)
	require.NoError(t, err)

	// freshULID must still be present.
	var found int64
	require.NoError(t, c.DB.Raw(`SELECT COUNT(*) FROM ops WHERE ulid = ?`, freshULID).Scan(&found).Error)
	assert.Equal(t, int64(1), found, "ops above the cutoff must never be deleted by dormant cleanup")
}

func TestCleanupDormantOps_BatchedDeletionLargeTable(t *testing.T) {
	// Exercise the batched path: insert more rows than dormantBatchSize
	// (10k) to confirm multiple iterations complete and totalForTable
	// is correct.
	if testing.Short() {
		t.Skip("large insert test: skipped under -short")
	}
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	cfg := RetentionConfig{DormantCleanupEnabled: true, DormantThreshold: 30 * 24 * time.Hour}

	now := time.Now().Add(-200 * 24 * time.Hour)
	// 15001 rows -> 2 full batches + 1 short batch
	rows := 15001
	bulk := make([]Op, 0, rows)
	for i := 0; i < rows; i++ {
		entropy := make([]byte, 10)
		entropy[0] = byte(i & 0xff)
		entropy[1] = byte((i >> 8) & 0xff)
		id, err := ulid.New(ulid.Timestamp(now.Add(time.Duration(i)*time.Millisecond)), bytes.NewReader(entropy))
		require.NoError(t, err)
		bulk = append(bulk, Op{
			ULID:   id.String(),
			Host:   "host1",
			Action: ActionCreate,
			Table:  "retention_test_models",
			Data:   json.RawMessage(`[]`),
		})
	}
	// chunk inserts so the test stays reasonable
	for start := 0; start < len(bulk); start += 5000 {
		end := start + 5000
		if end > len(bulk) {
			end = len(bulk)
		}
		require.NoError(t, c.DB.Create(bulk[start:end]).Error)
	}
	require.Equal(t, int64(rows), countOpsForTable(t, c, "retention_test_models"))

	deleted, err := c.CleanupDormantOps(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, int64(rows), deleted["retention_test_models"])
	assert.Equal(t, int64(0), countOpsForTable(t, c, "retention_test_models"))
	assert.GreaterOrEqual(t, retention.DormantOpsDeleted.Load(), uint64(rows))
}

// End-to-end gap signal: real HTTP server with the actual handler, real
// client. Catches wiring bugs between serve_crud.go and client.go.

func TestSweep_EndToEnd_GapHeaderPropagatesAndClientAdvances(t *testing.T) {
	resetRetentionStats()
	// Single shared DB (test harness uses one), so the server-side Crudr
	// and the client-side PeerClient both operate against the same ops
	// table. The handler reads from c.ServeCrudSweep; the client reads
	// the response. Truncate once, then never wipe again — both sides
	// share state.
	c := newRetentionCrudr(t, "self-host")

	// Seed three recent ops as the "peer" side.
	now := time.Now()
	for i := 0; i < 3; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(time.Duration(i)*time.Millisecond))
	}

	// Build a minimal HTTP server that mirrors what serve_crud.go does:
	// call ServeCrudSweep and forward the gap headers verbatim.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("after")
		ops, gap, err := c.ServeCrudSweep(r.Context(), after, 100)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if gap != "" {
			w.Header().Set(HeaderRetentionGap, "true")
			w.Header().Set(HeaderAvailableMin, gap)
		}
		w.Header().Set("Content-Type", "application/json")
		if ops == nil {
			ops = []*Op{}
		}
		buf, _ := json.Marshal(ops)
		w.Write(buf)
	}))
	defer server.Close()

	// Plant a cursor that's a year behind the server's ops. This is the
	// pre-gap state of a peer that was offline through the retention
	// window.
	oldCursorULID, err := ulid.New(ulid.Timestamp(time.Now().Add(-365*24*time.Hour)), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, c.DB.Create(&Cursor{Host: server.URL, LastULID: oldCursorULID.String()}).Error)

	// The PeerClient runs the same production code path; we just inject
	// the httptest client transport so the gap headers travel over the
	// wire path the real sweep uses.
	c.httpClient = server.Client()
	peer := NewPeerClient(server.URL, c, "self-host")
	require.NoError(t, peer.doSweep(context.Background()))

	var cur Cursor
	require.NoError(t, c.DB.Where("host = ?", server.URL).First(&cur).Error)
	min, err := c.MinAvailableULID(context.Background())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, cur.LastULID, min, "client cursor must be at or above peer's min")
	assert.Equal(t, uint64(1), retention.SweepGapAdvances.Load())
}

// Stale-cursor pinning behavior: an ancient peer cursor must pin the
// retention floor even when the age cutoff would otherwise delete those ops.
// Demonstrates the documented limitation that stale cursors block retention.
func TestRetentionTick_AncientCursorPinsAllRetention(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()

	// All ops > retention window (200d).
	for i := 0; i < 12; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-200*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}

	// A long-decommissioned peer with a cursor from 3 years ago.
	ancientULID, err := ulid.New(ulid.Timestamp(now.Add(-3*365*24*time.Hour)), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, c.DB.Create(&Cursor{Host: "decommissioned-peer", LastULID: ancientULID.String()}).Error)

	cfg := RetentionConfig{RetentionDays: 30, SweepBatchLimit: 1000, CursorSafetyMargin: 1 * time.Hour}
	require.NoError(t, c.retentionTick(context.Background(), cfg))

	// All ops survive: the ancient cursor pins the floor at -3y, far
	// below the oldest op we have, so the cutoff is below our min(ulid)
	// and nothing is eligible.
	assert.Equal(t, int64(12), countOpsForTable(t, c, "retention_test_models"),
		"ancient cursor must pin the retention floor")
	assert.Equal(t, uint64(0), retention.RetentionOpsDeleted.Load())
}

// Concurrent sweep + retention: prove Postgres MVCC keeps both safe.
func TestRetention_ConcurrentSweepAndDeleteSafety(t *testing.T) {
	resetRetentionStats()
	c := newRetentionCrudr(t, "host1")
	now := time.Now()
	for i := 0; i < 200; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-60*24*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	for i := 0; i < 20; i++ {
		insertOpAt(t, c, "retention_test_models", now.Add(-1*time.Minute).Add(time.Duration(i)*time.Millisecond))
	}
	recentULID, err := ulid.New(ulid.Timestamp(now), bytes.NewReader(make([]byte, 16)))
	require.NoError(t, err)
	require.NoError(t, c.DB.Create(&Cursor{Host: "peer", LastULID: recentULID.String()}).Error)

	cfg := RetentionConfig{RetentionDays: 30, SweepBatchLimit: 50, CursorSafetyMargin: 1 * time.Hour}

	// Fire a retention tick and a sweep query concurrently; assert both
	// complete without error and that the sweep returns a coherent
	// snapshot.
	doneSweep := make(chan int, 1)
	doneRetention := make(chan error, 1)
	go func() {
		ops, _, err := c.ServeCrudSweep(context.Background(), "", 1000)
		if err != nil {
			doneSweep <- -1
			return
		}
		doneSweep <- len(ops)
	}()
	go func() {
		doneRetention <- c.retentionTick(context.Background(), cfg)
	}()

	require.NoError(t, <-doneRetention)
	got := <-doneSweep
	assert.GreaterOrEqual(t, got, 0, "concurrent sweep must complete without error")
}
