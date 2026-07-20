package opvalidation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidatePayloadAcceptsRegisteredTables(t *testing.T) {
	cases := []struct {
		name  string
		table string
		data  []byte
	}{
		{
			name:  "uploads",
			table: "uploads",
			data:  []byte(`[{"id":"cid","mirrors":[],"transcoded_mirrors":[],"placement_hosts":[]}]`),
		},
		{
			name:  "storage and db sizes",
			table: "storage_and_db_sizes",
			data:  []byte(`[{"Host":"https://node.example","StorageBackend":"s3"}]`),
		},
		{
			name:  "qm audio analyses",
			table: "qm_audio_analyses",
			data:  []byte(`[{"cid":"cid","mirrors":[],"status":"done"}]`),
		},
		{
			name:  "audio previews",
			table: "audio_previews",
			data:  []byte(`[{"cid":"preview","SourceCID":"source"}]`),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, ValidatePayload("update", tc.table, tc.data))
		})
	}
}

func TestValidateOperationRejectsUncommittablePayloads(t *testing.T) {
	validULID := "01JY0000000000000000000000"
	validHost := "https://node.example"
	validTable := "uploads"
	validData := []byte(`[{"id":"cid","mirrors":[]}]`)

	cases := []struct {
		name   string
		ulid   string
		host   string
		action string
		table  string
		data   []byte
	}{
		{
			name:   "invalid ulid",
			ulid:   "not-a-ulid",
			host:   validHost,
			action: "update",
			table:  validTable,
			data:   validData,
		},
		{
			name:   "uppercase action",
			ulid:   validULID,
			host:   validHost,
			action: "UPDATE",
			table:  validTable,
			data:   validData,
		},
		{
			name:   "unknown table",
			ulid:   validULID,
			host:   validHost,
			action: "update",
			table:  "not_a_registered_model",
			data:   validData,
		},
		{
			name:   "malformed json",
			ulid:   validULID,
			host:   validHost,
			action: "update",
			table:  validTable,
			data:   []byte(`{`),
		},
		{
			name:   "object instead of array",
			ulid:   validULID,
			host:   validHost,
			action: "update",
			table:  validTable,
			data:   []byte(`{"id":"cid"}`),
		},
		{
			name:   "bad field type",
			ulid:   validULID,
			host:   validHost,
			action: "update",
			table:  validTable,
			data:   []byte(`[{"id":"cid","mirrors":"not-an-array"}]`),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, ValidateOperation(tc.ulid, tc.host, tc.action, tc.table, tc.data))
		})
	}
}

func TestValidateCorePayloadSize(t *testing.T) {
	require.NoError(t, ValidateCorePayloadSize(make([]byte, MaxCoreOperationDataBytes)))
	require.Error(t, ValidateCorePayloadSize(make([]byte, MaxCoreOperationDataBytes+1)))
}
