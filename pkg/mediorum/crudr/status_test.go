package crudr

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestStatusReportsHistoryBoundsAndCursorSafety(t *testing.T) {
	db := SetupTestDB()
	require.NoError(t, db.AutoMigrate(TestBlobThing{}))

	z := zap.NewNop()
	c := New("host1", db, z).
		RegisterModels(&TestBlobThing{})
	require.NoError(t, db.Exec("TRUNCATE ops, cursors, test_blob_things").Error)

	require.NoError(t, c.Create(TestBlobThing{Host: "server1", Key: "dd1"}))
	require.NoError(t, c.Create(TestBlobThing{Host: "server1", Key: "dd2"}))
	require.NoError(t, c.DB.Create(&Cursor{Host: "peer-empty", LastULID: ""}).Error)
	require.NoError(t, c.DB.Create(&Cursor{Host: "peer-old", LastULID: "01GW5C4Y87PH83C66AHB0VDBN4"}).Error)
	require.NoError(t, c.DB.Create(&Cursor{Host: "peer-new", LastULID: "01KRXB1K24BAFDYCGR07KDHTQ5"}).Error)

	status, err := c.Status(context.Background(), []string{"ops", "test_blob_things", "cursors"})
	require.NoError(t, err)

	require.NotEmpty(t, status.MinAvailableULID)
	require.NotEmpty(t, status.MaxULID)
	require.LessOrEqual(t, status.MinAvailableULID, status.MaxULID)

	require.Equal(t, int64(3), status.Cursors.Count)
	require.Equal(t, int64(1), status.Cursors.NullOrEmptyCount)
	require.Equal(t, "01GW5C4Y87PH83C66AHB0VDBN4", status.Cursors.MinLastULID)
	require.Equal(t, "01KRXB1K24BAFDYCGR07KDHTQ5", status.Cursors.MaxLastULID)

	require.Contains(t, status.Tables, "ops")
	require.Contains(t, status.Tables, "test_blob_things")
	require.Contains(t, status.Tables, "cursors")
	require.GreaterOrEqual(t, status.Tables["ops"].TotalBytes, int64(0))
}

func TestStatusReportsEmptyAvailableHistory(t *testing.T) {
	db := SetupTestDB()

	z := zap.NewNop()
	c := New("host1", db, z)
	require.NoError(t, db.Exec("TRUNCATE ops, cursors").Error)

	status, err := c.Status(context.Background(), []string{"ops", "cursors"})
	require.NoError(t, err)

	require.Empty(t, status.MinAvailableULID)
	require.Empty(t, status.MaxULID)
	require.Equal(t, int64(0), status.Cursors.Count)
	require.Equal(t, int64(0), status.Cursors.NullOrEmptyCount)
	require.Contains(t, status.Tables, "ops")
}

func TestStatusClampsNegativeReltuples(t *testing.T) {
	// Newly-created Postgres tables have pg_class.reltuples = -1 until ANALYZE
	// runs. The status query must clamp that to 0 instead of leaking a
	// sentinel value to peers.
	db := SetupTestDB()

	z := zap.NewNop()
	c := New("host1", db, z)

	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS status_reltuples_probe (id bigint)`).Error)
	t.Cleanup(func() {
		require.NoError(t, db.Exec(`DROP TABLE IF EXISTS status_reltuples_probe`).Error)
	})
	// Force the sentinel to make the test deterministic even on databases that
	// have already ANALYZEd the relation.
	require.NoError(t, db.Exec(`UPDATE pg_class SET reltuples = -1 WHERE relname = 'status_reltuples_probe'`).Error)

	status, err := c.Status(context.Background(), []string{"status_reltuples_probe"})
	require.NoError(t, err)
	probe, ok := status.Tables["status_reltuples_probe"]
	require.True(t, ok)
	require.Equal(t, int64(0), probe.EstimatedRows)
}
