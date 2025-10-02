package integration_tests

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/core/server"
	"github.com/OpenAudio/go-openaudio/pkg/integration_tests/utils"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestEntityManager(t *testing.T) {
	ctx := context.Background()
	sdk := utils.DiscoveryOne

	err := utils.WaitForDevnetHealthy(60 * time.Second)
	assert.NoError(t, err)

	// Generate a test private key
	privateKey, err := crypto.GenerateKey()
	assert.NoError(t, err)

	// Get the expected signer address
	expectedSigner := crypto.PubkeyToAddress(privateKey.PublicKey).String()

	// Use dev config values for signing (must match what the server uses)
	mockConfig := &config.Config{
		AcdcEntityManagerAddress: config.DevAcdcAddress,
		AcdcChainID:              config.DevAcdcChainID,
	}

	t.Run("ValidSignature", func(t *testing.T) {
		manageEntity := &corev1.ManageEntityLegacy{
			UserId:     1,
			EntityType: "User",
			EntityId:   1,
			Action:     "Create",
			Metadata:   "some json",
			Nonce:      "0x0000000000000000000000000000000000000000000000000000000000000001",
			Signer:     "0x123", // This will be overwritten by InjectSigner
		}

		// Sign the message with EIP712
		err = server.SignManageEntity(mockConfig, manageEntity, privateKey)
		assert.NoError(t, err)

		signedManageEntity := &corev1.SignedTransaction{
			RequestId: uuid.NewString(),
			Transaction: &corev1.SignedTransaction_ManageEntity{
				ManageEntity: manageEntity,
			},
		}

		req := &corev1.SendTransactionRequest{
			Transaction: signedManageEntity,
		}

		submitRes, err := sdk.Core.SendTransaction(ctx, connect.NewRequest(req))
		if !assert.NoError(t, err) {
			return // Exit early if there's an error to avoid nil pointer
		}

		// The signer should now be the address recovered from the signature
		actualSigner := submitRes.Msg.Transaction.Transaction.GetManageEntity().Signer
		t.Logf("Expected signer: %s", expectedSigner)
		t.Logf("Actual signer: %s", actualSigner)
		assert.Equal(t, expectedSigner, actualSigner)

		txhash := submitRes.Msg.Transaction.Hash

		time.Sleep(time.Second * 1)

		manageEntityRes, err := sdk.Core.GetTransaction(ctx, connect.NewRequest(&corev1.GetTransactionRequest{TxHash: txhash}))
		assert.NoError(t, err)

		// Verify the signer persisted correctly
		assert.Equal(t, expectedSigner, manageEntityRes.Msg.Transaction.Transaction.GetManageEntity().Signer)
	})

	t.Run("InvalidSignature", func(t *testing.T) {
		manageEntity := &corev1.ManageEntityLegacy{
			UserId:     2,
			EntityType: "User",
			EntityId:   2,
			Action:     "Create",
			Metadata:   "some json",
			Signature:  "invalid_signature", // Invalid signature
			Nonce:      "2",
			Signer:     "0x456", // This won't be overwritten since signature is invalid
		}

		signedManageEntity := &corev1.SignedTransaction{
			RequestId: uuid.NewString(),
			Transaction: &corev1.SignedTransaction_ManageEntity{
				ManageEntity: manageEntity,
			},
		}

		req := &corev1.SendTransactionRequest{
			Transaction: signedManageEntity,
		}

		_, err := sdk.Core.SendTransaction(ctx, connect.NewRequest(req))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "signer not recoverable")
	})
}
