package server

import (
	"context"
	"crypto/ecdsa"
	"testing"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/opvalidation"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

func TestIsValidMediorumOperationTx(t *testing.T) {
	pool := setupValidatorTestDB(t)
	truncateValidators(t, pool)

	ctx := context.Background()
	q := db.New(pool)
	key, err := gethcrypto.GenerateKey()
	require.NoError(t, err)
	wallet := gethcrypto.PubkeyToAddress(key.PublicKey).Hex()
	require.NoError(t, q.InsertRegisteredNode(ctx, db.InsertRegisteredNodeParams{
		PubKey:       "mediorum-pubkey",
		Endpoint:     "https://node.example.com",
		EthAddress:   wallet,
		CometAddress: "MEDIORUMCOMETADDRESS",
		CometPubKey:  "mediorum-comet-pubkey",
		EthBlock:     "100",
		NodeType:     "content-node",
		SpID:         "1",
	}))

	s := &Server{db: q, logger: zap.NewNop()}
	validOp := &v1.MediorumOperation{
		Ulid:   "01JY0000000000000000000000",
		Host:   "https://node.example.com/",
		Action: "update",
		Table:  "uploads",
		Data:   []byte(`[{"id":"cid","mirrors":[]}]`),
	}

	require.NoError(t, s.isValidMediorumOperationTx(ctx, signMediorumOperationForTest(t, validOp, key)))

	unknownTable := proto.Clone(validOp).(*v1.MediorumOperation)
	unknownTable.Table = "not_a_registered_model"
	require.Error(t, s.isValidMediorumOperationTx(ctx, signMediorumOperationForTest(t, unknownTable, key)))

	otherKey, err := gethcrypto.GenerateKey()
	require.NoError(t, err)
	require.Error(t, s.isValidMediorumOperationTx(ctx, signMediorumOperationForTest(t, validOp, otherKey)))
}

func TestValidateMediorumOperationSubmissionSize(t *testing.T) {
	require.NoError(t, validateMediorumOperationSubmissionSize(&v1.MediorumOperation{
		Data: make([]byte, opvalidation.MaxCoreOperationDataBytes),
	}))
	require.Error(t, validateMediorumOperationSubmissionSize(&v1.MediorumOperation{
		Data: make([]byte, opvalidation.MaxCoreOperationDataBytes+1),
	}))
}

func signMediorumOperationForTest(t *testing.T, op *v1.MediorumOperation, key *ecdsa.PrivateKey) *v1.SignedTransaction {
	t.Helper()

	bodyBytes, err := proto.Marshal(op)
	require.NoError(t, err)
	sig, err := common.EthSign(key, bodyBytes)
	require.NoError(t, err)
	return &v1.SignedTransaction{
		Signature: sig,
		Transaction: &v1.SignedTransaction_MediorumOperation{
			MediorumOperation: op,
		},
	}
}
