package server

import (
	"context"
	"testing"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
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
