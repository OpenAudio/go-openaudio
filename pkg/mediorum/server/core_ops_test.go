package server

import (
	"context"
	"testing"

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
