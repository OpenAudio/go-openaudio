package crudr

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/lifecycle"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// example of a "user type" that is hooked up with crudr
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
	c := New("host1", nil, nil, db, lifecycle.NewLifecycle(context.Background(), "crudr test", z), z, nil).RegisterModels(&TestBlobThing{})

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
		assert.Equal(t, CoreTxStatusLocal, ops[0].CoreTxStatus)
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

	z := zap.NewNop()
	c := New("host1", nil, nil, db, lifecycle.NewLifecycle(ctx, "crudr test", z), z, nil).RegisterModels(&TestBlobThing{})
	c.SetCoreWritesEnabled(true)

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

func TestRejectedCoreOpIsNotRetried(t *testing.T) {
	ctx := context.Background()
	db := SetupTestDB()
	require.NoError(t, db.AutoMigrate(TestBlobThing{}))

	z := zap.NewNop()
	c := New("host1", nil, nil, db, lifecycle.NewLifecycle(ctx, "crudr test", z), z, nil).RegisterModels(&TestBlobThing{})
	c.SetCoreWritesEnabled(true)
	require.NoError(t, c.Create(TestBlobThing{Host: "server1", Key: "oversized"}))

	var op Op
	require.NoError(t, c.DB.First(&op).Error)
	rejectedAt := time.Now().UTC().Truncate(time.Second)
	rejection := errors.New("operation data exceeds limit")
	require.NoError(t, c.MarkCoreRejected(ctx, &op, rejectedAt, rejection))

	pending, err := c.PendingCoreOps(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, pending)

	var rejected Op
	require.NoError(t, c.DB.First(&rejected, "ulid = ?", op.ULID).Error)
	require.Equal(t, CoreTxStatusRejected, rejected.CoreTxStatus)
	require.Equal(t, rejection.Error(), rejected.CoreTxError)
	require.NotNil(t, rejected.CoreAttemptedAt)
	require.True(t, rejected.CoreAttemptedAt.Equal(rejectedAt))
}

func TestValidateOpRejectsUnapplicableOp(t *testing.T) {
	c := &Crudr{
		typeMap: map[string]reflect.Type{
			"test_blob_things": reflect.TypeOf(TestBlobThing{}),
		},
	}

	valid := &Op{
		Action: ActionUpdate,
		Table:  "test_blob_things",
		Data:   json.RawMessage(`[{"Key":"k","Host":"h"}]`),
	}
	require.NoError(t, c.ValidateOp(valid))

	cases := []struct {
		name string
		op   *Op
	}{
		{
			name: "nil op",
			op:   nil,
		},
		{
			name: "unknown action",
			op:   &Op{Action: "UPDATE", Table: valid.Table, Data: valid.Data},
		},
		{
			name: "unknown table",
			op:   &Op{Action: valid.Action, Table: "not_a_registered_model", Data: valid.Data},
		},
		{
			name: "malformed data",
			op:   &Op{Action: valid.Action, Table: valid.Table, Data: json.RawMessage(`{`)},
		},
		{
			name: "object data",
			op:   &Op{Action: valid.Action, Table: valid.Table, Data: json.RawMessage(`{"Key":"k","Host":"h"}`)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, c.ValidateOp(tc.op))
		})
	}
}
