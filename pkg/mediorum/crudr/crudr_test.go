package crudr

import (
	"context"
	"fmt"
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
	c := New("https://self.example", nil, nil, db, lifecycle.NewLifecycle(context.Background(), "crudr retry op test", z), z, nil).
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
}

func TestLegacyTransientUploadRetryOpsPersistForLocalHost(t *testing.T) {
	db := SetupTestDB()

	require.NoError(t, db.AutoMigrate(Upload{}))
	require.NoError(t, db.Exec("TRUNCATE ops, uploads").Error)

	z := zap.NewNop()
	c := New("https://self.example", nil, nil, db, lifecycle.NewLifecycle(context.Background(), "crudr local retry op test", z), z, nil).
		RegisterModels(&Upload{})

	op := &Op{
		ULID:   "01KTC3Y9SW2GND1R4QZW2SAS02",
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
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := SetupTestDB()

			require.NoError(t, db.AutoMigrate(Upload{}))
			require.NoError(t, db.Exec("TRUNCATE ops, uploads").Error)

			z := zap.NewNop()
			c := New("https://self.example", nil, nil, db, lifecycle.NewLifecycle(context.Background(), "crudr durable upload op test", z), z, nil).
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
