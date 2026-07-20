package server

import (
	"bytes"
	"context"
	"testing"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/opvalidation"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

func applyCoreOpsFrom(t *testing.T, origin *MediorumServer) {
	t.Helper()

	var ops []crudr.Op
	require.NoError(t, origin.crud.DB.Order("ulid asc").Find(&ops, "host = ?", origin.Config.Self.Host).Error)

	for i := range ops {
		msg := mediorumOperationFromOp(&ops[i])
		for _, target := range testNetwork {
			require.NoError(t, target.ApplyMediorumOperation(context.Background(), msg, "test-core-tx-"+ops[i].ULID))
		}
	}
}

func TestApplyMediorumOperationRejectsInvalidPayload(t *testing.T) {
	invalid := &corev1.MediorumOperation{
		Ulid:   "01JY0000000000000000000000",
		Host:   "https://node.example",
		Action: "UPDATE",
		Table:  "uploads",
		Data:   []byte(`[{"id":"cid"}]`),
	}

	require.Error(t, testNetwork[0].ApplyMediorumOperation(context.Background(), invalid, "test-core-tx-invalid"))
}

func TestApplyCommittedCoreMediorumOperationSkipsInvalidPayload(t *testing.T) {
	invalid := &corev1.MediorumOperation{
		Ulid:   "01JY0000000000000000000000",
		Host:   "https://node.example",
		Action: "update",
		Table:  "not_a_registered_model",
		Data:   []byte(`[{"id":"cid"}]`),
	}

	require.NoError(t, (&MediorumServer{}).applyCommittedCoreMediorumOperation(context.Background(), 42, "test-core-tx-invalid", invalid))
}

func TestSubmitCoreOpRejectsOversizedData(t *testing.T) {
	server := testNetwork[0]
	data := append([]byte(`[{"id":"`), bytes.Repeat([]byte("x"), opvalidation.MaxCoreOperationDataBytes)...)
	data = append(data, []byte(`"}]`)...)
	op := &crudr.Op{
		ULID:         ulid.Make().String(),
		Host:         server.Config.Self.Host,
		Action:       crudr.ActionUpdate,
		Table:        "uploads",
		Data:         data,
		CoreTxStatus: crudr.CoreTxStatusPending,
	}
	require.NoError(t, server.crud.DB.Create(op).Error)
	t.Cleanup(func() {
		server.crud.DB.Delete(&crudr.Op{}, "ulid = ?", op.ULID)
	})

	require.Error(t, server.submitCoreOp(context.Background(), op))

	var rejected crudr.Op
	require.NoError(t, server.crud.DB.First(&rejected, "ulid = ?", op.ULID).Error)
	require.Equal(t, crudr.CoreTxStatusRejected, rejected.CoreTxStatus)
	require.Contains(t, rejected.CoreTxError, "maximum is 65536")
}
