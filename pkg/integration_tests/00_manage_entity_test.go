package integration_tests

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/integration_tests/utils"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

func TestEntityManager(t *testing.T) {
	ctx := context.Background()

	sdk := utils.DiscoveryOne

	manageEntity := &corev1.ManageEntityLegacy{
		UserId:     1,
		EntityType: "User",
		EntityId:   1,
		Action:     "Create",
		Metadata:   "some json",
		Signature:  "eip712",
		Nonce:      "1",
		Signer:     "0x123",
	}

	signedManageEntity := &corev1.SignedTransaction{
		RequestId: uuid.NewString(),
		Transaction: &corev1.SignedTransaction_ManageEntity{
			ManageEntity: manageEntity,
		},
	}

	expectedTxHash, err := common.ToTxHash(signedManageEntity)
	assert.NoError(t, err)

	err = utils.WaitForDevnetHealthy(60 * time.Second)
	assert.NoError(t, err)

	req := &corev1.SendTransactionRequest{
		Transaction: signedManageEntity,
	}

	submitRes, err := sdk.Core.SendTransaction(ctx, connect.NewRequest(req))
	assert.NoError(t, err)

	txhash := submitRes.Msg.Transaction.Hash
	assert.Equal(t, expectedTxHash, txhash)

	time.Sleep(time.Second * 1)

	manageEntityRes, err := sdk.Core.GetTransaction(ctx, connect.NewRequest(&corev1.GetTransactionRequest{TxHash: txhash}))
	assert.NoError(t, err)

	assert.True(t, proto.Equal(signedManageEntity, manageEntityRes.Msg.Transaction.Transaction))
}
