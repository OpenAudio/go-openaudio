package crudr

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// example of a "user type" that is hooked up with the operation log
type TestBlobThing struct {
	Key       string         `gorm:"primaryKey;not null;default:null"`
	Host      string         `gorm:"primaryKey;not null;default:null"`
	CreatedAt time.Time      `gorm:"index"`
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func TestCrudr(t *testing.T) {

	db := SetupTestDB()

	err := db.AutoMigrate(TestBlobThing{})
	assert.NoError(t, err)

	z := zap.NewNop()
	c := New("host1", db, z).RegisterModels(&TestBlobThing{})

	// table name
	{
		assert.Equal(t, "test_blob_things", c.tableNameFor(TestBlobThing{}))
		assert.Equal(t, "test_blob_things", c.tableNameFor(&TestBlobThing{}))
		assert.Equal(t, "test_blob_things", c.tableNameFor([]TestBlobThing{}))
		assert.Equal(t, "test_blob_things", c.tableNameFor([]*TestBlobThing{}))
		assert.Equal(t, "test_blob_things", c.tableNameFor(&[]*TestBlobThing{}))
	}

	err = c.Create([]TestBlobThing{
		{
			Host: "server1",
			Key:  "dd1",
		},
		{
			Host: "server1",
			Key:  "dd2",
		},
	})
	assert.NoError(t, err)

	err = c.Create(
		[]*TestBlobThing{
			{
				Host: "server1",
				Key:  "dd3",
			},
		},
		WithTransient())
	assert.NoError(t, err)

	{
		var ops []Op
		c.DB.Find(&ops)
		assert.Len(t, ops, 1)
		assert.Equal(t, CoreTxStatusPending, ops[0].CoreTxStatus)
	}

	{
		var blobs []TestBlobThing
		c.DB.Find(&blobs)
		assert.Len(t, blobs, 3)
	}
}

func TestCoreRelayState(t *testing.T) {
	ctx := context.Background()
	db := SetupTestDB()
	require.NoError(t, db.AutoMigrate(TestBlobThing{}))

	c := New("host1", db, zap.NewNop()).RegisterModels(&TestBlobThing{})

	require.NoError(t, c.Create(TestBlobThing{Host: "server1", Key: "dd1"}))

	var op Op
	require.NoError(t, c.DB.First(&op).Error)
	require.Equal(t, CoreTxStatusPending, op.CoreTxStatus)

	attemptedAt := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, c.MarkCoreError(ctx, &op, "tx1", attemptedAt, assert.AnError))

	pending, err := c.PendingCoreOps(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, CoreTxStatusError, pending[0].CoreTxStatus)
	require.Equal(t, "tx1", pending[0].CoreTxHash)

	confirmedAt := attemptedAt.Add(time.Minute)
	confirmed := op
	confirmed.CoreTxHash = "tx2"
	confirmed.CoreTxStatus = CoreTxStatusConfirmed
	confirmed.CoreTxError = ""
	confirmed.CoreConfirmedAt = &confirmedAt
	require.NoError(t, c.ApplyOp(&confirmed))

	var updated Op
	require.NoError(t, c.DB.First(&updated, "ulid = ?", op.ULID).Error)
	require.Equal(t, CoreTxStatusConfirmed, updated.CoreTxStatus)
	require.Equal(t, "tx2", updated.CoreTxHash)
	require.Empty(t, updated.CoreTxError)
	require.NotNil(t, updated.CoreConfirmedAt)
	require.True(t, updated.CoreConfirmedAt.Equal(confirmedAt))
}
