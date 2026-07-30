package crudr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

type Upload struct {
	ID         string            `json:"id" gorm:"primaryKey"`
	Status     string            `json:"status"`
	ErrorCount int               `json:"error_count"`
	Results    map[string]string `json:"results" gorm:"serializer:json"`
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

func TestRemoteLegacyTransientUploadRetryOpsApplyWithoutPersisting(t *testing.T) {
	db := SetupTestDB()

	require.NoError(t, db.AutoMigrate(Upload{}))
	require.NoError(t, db.Exec("TRUNCATE ops, uploads").Error)

	z := zap.NewNop()
	c := New("https://self.example", db, z).
		RegisterModels(&Upload{})

	op := &Op{
		ULID:   "01KTC3Y9SW2GND1R4QZW2SAS01",
		Host:   "https://peer.example",
		Action: ActionUpdate,
		Table:  "uploads",
		Data:   []byte(`[{"id":"upload-1","status":"busy","error_count":6,"results":{}}]`),
	}

	require.NoError(t, c.ApplyOp(op))

	var opsCount int64
	require.NoError(t, db.Model(&Op{}).Count(&opsCount).Error)
	require.Zero(t, opsCount)

	var upload Upload
	require.NoError(t, db.First(&upload, "id = ?", "upload-1").Error)
	require.Equal(t, "busy", upload.Status)
	require.Equal(t, 6, upload.ErrorCount)

	op = &Op{
		ULID:   "01KTC3Y9SW2GND1R4QZW2SAS02",
		Host:   "https://peer.example",
		Action: ActionUpdate,
		Table:  "uploads",
		Data:   []byte(`[{"id":"upload-2","status":"error","error_count":6}]`),
	}

	require.NoError(t, c.ApplyOp(op))
	require.NoError(t, db.Model(&Op{}).Count(&opsCount).Error)
	require.Zero(t, opsCount)

	upload = Upload{}
	require.NoError(t, db.First(&upload, "id = ?", "upload-2").Error)
	require.Equal(t, "error", upload.Status)
	require.Equal(t, 6, upload.ErrorCount)
}

func TestLegacyTransientUploadRetryOpsPersistForLocalHost(t *testing.T) {
	db := SetupTestDB()

	require.NoError(t, db.AutoMigrate(Upload{}))
	require.NoError(t, db.Exec("TRUNCATE ops, uploads").Error)

	z := zap.NewNop()
	c := New("https://self.example", db, z).
		RegisterModels(&Upload{})

	op := &Op{
		ULID:   "01KTC3Y9SW2GND1R4QZW2SAS03",
		Host:   "https://self.example",
		Action: ActionUpdate,
		Table:  "uploads",
		Data:   []byte(`[{"id":"upload-1","status":"error","error_count":6,"results":{}}]`),
	}

	require.NoError(t, c.ApplyOp(op))

	var opsCount int64
	require.NoError(t, db.Model(&Op{}).Count(&opsCount).Error)
	require.EqualValues(t, 1, opsCount)
}

func TestRemoteUploadRetryOpsPersistWhenNotLegacyTransient(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			name: "retry limit not exceeded",
			data: `[{"id":"upload-1","status":"error","error_count":5,"results":{}}]`,
		},
		{
			name: "transcode result exists",
			data: `[{"id":"upload-1","status":"error","error_count":6,"results":{"320":"cid-320"}}]`,
		},
		{
			name: "final done",
			data: `[{"id":"upload-1","status":"done","error_count":6,"results":{}}]`,
		},
		{
			name: "mixed batch has durable row",
			data: `[{"id":"upload-1","status":"error","error_count":6,"results":{}},{"id":"upload-2","status":"done","error_count":6,"results":{}}]`,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := SetupTestDB()

			require.NoError(t, db.AutoMigrate(Upload{}))
			require.NoError(t, db.Exec("TRUNCATE ops, uploads").Error)

			z := zap.NewNop()
			c := New("https://self.example", db, z).
				RegisterModels(&Upload{})

			op := &Op{
				ULID:   fmt.Sprintf("01KTC3Y9SW2GND1R4QZW2SAS%02d", i+3),
				Host:   "https://peer.example",
				Action: ActionUpdate,
				Table:  "uploads",
				Data:   []byte(tt.data),
			}

			require.NoError(t, c.ApplyOp(op))

			var opsCount int64
			require.NoError(t, db.Model(&Op{}).Count(&opsCount).Error)
			require.EqualValues(t, 1, opsCount)
		})
	}
}

func TestLegacyTransientUploadRetryOpClassifierIsNarrow(t *testing.T) {
	tests := []struct {
		name string
		op   Op
		want bool
	}{
		{
			name: "busy retry without result",
			op: Op{
				Action: ActionUpdate,
				Table:  "uploads",
				Data:   []byte(`[{"status":"busy","error_count":6,"results":{}}]`),
			},
			want: true,
		},
		{
			name: "error retry without results field",
			op: Op{
				Action: ActionUpdate,
				Table:  "uploads",
				Data:   []byte(`[{"status":"error","error_count":6}]`),
			},
			want: true,
		},
		{
			name: "retry limit boundary is durable",
			op: Op{
				Action: ActionUpdate,
				Table:  "uploads",
				Data:   []byte(`[{"status":"error","error_count":5,"results":{}}]`),
			},
		},
		{
			name: "transcode result is durable",
			op: Op{
				Action: ActionUpdate,
				Table:  "uploads",
				Data:   []byte(`[{"status":"error","error_count":6,"results":{"320":"cid-320"}}]`),
			},
		},
		{
			name: "done status is durable",
			op: Op{
				Action: ActionUpdate,
				Table:  "uploads",
				Data:   []byte(`[{"status":"done","error_count":6,"results":{}}]`),
			},
		},
		{
			name: "mixed batch is durable",
			op: Op{
				Action: ActionUpdate,
				Table:  "uploads",
				Data:   []byte(`[{"status":"error","error_count":6,"results":{}},{"status":"done","error_count":6,"results":{}}]`),
			},
		},
		{
			name: "wrong table is durable",
			op: Op{
				Action: ActionUpdate,
				Table:  "qm_audio_analyses",
				Data:   []byte(`[{"status":"error","error_count":6,"results":{}}]`),
			},
		},
		{
			name: "create is durable",
			op: Op{
				Action: ActionCreate,
				Table:  "uploads",
				Data:   []byte(`[{"status":"error","error_count":6,"results":{}}]`),
			},
		},
		{
			name: "malformed json is durable",
			op: Op{
				Action: ActionUpdate,
				Table:  "uploads",
				Data:   []byte(`{"status":"error","error_count":6,"results":{}}`),
			},
		},
		{
			name: "unexpected results shape is durable",
			op: Op{
				Action: ActionUpdate,
				Table:  "uploads",
				Data:   []byte(`[{"status":"error","error_count":6,"results":[]}]`),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isLegacyTransientUploadRetryOp(&tt.op))
		})
	}
}

func TestSuppressedRetryOpsDoNotPoisonReplayHistory(t *testing.T) {
	db := SetupTestDB()

	require.NoError(t, db.AutoMigrate(Upload{}))
	require.NoError(t, db.Exec("TRUNCATE ops, uploads").Error)

	z := zap.NewNop()
	c := New("https://self.example", db, z).
		RegisterModels(&Upload{})

	require.NoError(t, c.ApplyOp(&Op{
		ULID:   "01KTC3Y9SW2GND1R4QZW2SAS10",
		Host:   "https://peer.example",
		Action: ActionUpdate,
		Table:  "uploads",
		Data:   []byte(`[{"id":"upload-1","status":"busy","error_count":6,"results":{}}]`),
	}))
	require.NoError(t, c.ApplyOp(&Op{
		ULID:   "01KTC3Y9SW2GND1R4QZW2SAS11",
		Host:   "https://peer.example",
		Action: ActionUpdate,
		Table:  "uploads",
		Data:   []byte(`[{"id":"upload-1","status":"done","error_count":6,"results":{"320":"cid-320"}}]`),
	}))

	var persisted []Op
	require.NoError(t, db.Order("ulid ASC").Find(&persisted).Error)
	require.Len(t, persisted, 1)
	require.Equal(t, "01KTC3Y9SW2GND1R4QZW2SAS11", persisted[0].ULID)

	var live Upload
	require.NoError(t, db.First(&live, "id = ?", "upload-1").Error)
	require.Equal(t, "done", live.Status)
	require.Equal(t, "cid-320", live.Results["320"])

	require.NoError(t, db.Exec("TRUNCATE ops, uploads").Error)

	replay := New("https://fresh.example", db, z).
		RegisterModels(&Upload{})
	for i := range persisted {
		require.NoError(t, replay.ApplyOp(&persisted[i]))
	}

	var replayed Upload
	require.NoError(t, db.First(&replayed, "id = ?", "upload-1").Error)
	require.Equal(t, "done", replayed.Status)
	require.Equal(t, 6, replayed.ErrorCount)
	require.Equal(t, "cid-320", replayed.Results["320"])
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

func TestRejectedCoreOpIsNotRetried(t *testing.T) {
	ctx := context.Background()
	db := SetupTestDB()
	require.NoError(t, db.AutoMigrate(TestBlobThing{}))

	c := New("host1", db, zap.NewNop()).RegisterModels(&TestBlobThing{})
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
