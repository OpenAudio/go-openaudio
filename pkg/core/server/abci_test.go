package server

import (
	"context"
	"testing"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func marshalSignedTx(t *testing.T, tx *v1.SignedTransaction) []byte {
	t.Helper()
	b, err := proto.Marshal(tx)
	require.NoError(t, err)
	return b
}

func checkTxCode(t *testing.T, tx []byte) uint32 {
	t.Helper()
	res, err := (&Server{}).CheckTx(context.Background(), &abcitypes.CheckTxRequest{Tx: tx})
	require.NoError(t, err)
	return res.Code
}

func TestIsValidSignedTransaction_ParseOnly(t *testing.T) {
	s := &Server{}

	t.Run("nil transaction body", func(t *testing.T) {
		tx := marshalSignedTx(t, &v1.SignedTransaction{})
		msg, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
		require.Nil(t, msg.Transaction)
		require.Equal(t, uint32(1), checkTxCode(t, tx))
	})

	t.Run("invalid protobuf", func(t *testing.T) {
		_, err := s.isValidSignedTransaction([]byte("not protobuf"))
		require.Error(t, err)
	})
}

func TestIsValidSignedTransaction_StorageProof(t *testing.T) {
	s := &Server{}

	marshal := func(t *testing.T, sp *v1.StorageProof) []byte {
		t.Helper()
		return marshalSignedTx(t, &v1.SignedTransaction{
			Transaction: &v1.SignedTransaction_StorageProof{StorageProof: sp},
		})
	}

	t.Run("valid", func(t *testing.T) {
		tx := marshal(t, &v1.StorageProof{
			Height:          100,
			Address:         "ABCD1234",
			ProverAddresses: []string{"ADDR1", "ADDR2"},
			Cid:             "QmTest",
			ProofSignature:  []byte("sig"),
		})
		msg, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
		require.Len(t, msg.GetStorageProof().ProverAddresses, 2)
	})

	t.Run("empty prover addresses", func(t *testing.T) {
		tx := marshal(t, &v1.StorageProof{Height: 100, Address: "ABCD1234"})
		msg, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
		require.Empty(t, msg.GetStorageProof().ProverAddresses)
		require.Equal(t, uint32(1), checkTxCode(t, tx))
	})

	t.Run("nil prover addresses after proto roundtrip", func(t *testing.T) {
		tx := marshal(t, &v1.StorageProof{Height: 100, Address: "ABCD1234", ProverAddresses: nil})
		msg, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
		require.Empty(t, msg.GetStorageProof().ProverAddresses)
		require.Equal(t, uint32(1), checkTxCode(t, tx))
	})

	t.Run("empty slice prover addresses after proto roundtrip", func(t *testing.T) {
		tx := marshal(t, &v1.StorageProof{Height: 100, Address: "ABCD1234", ProverAddresses: []string{}})
		msg, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
		require.Empty(t, msg.GetStorageProof().ProverAddresses)
		require.Equal(t, uint32(1), checkTxCode(t, tx))
	})

	t.Run("missing address", func(t *testing.T) {
		tx := marshal(t, &v1.StorageProof{Height: 100, ProverAddresses: []string{"ADDR1"}})
		_, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
		require.Equal(t, uint32(1), checkTxCode(t, tx))
	})

	t.Run("zero height", func(t *testing.T) {
		tx := marshal(t, &v1.StorageProof{Address: "ABCD1234", ProverAddresses: []string{"ADDR1"}})
		_, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
		require.Equal(t, uint32(1), checkTxCode(t, tx))
	})
}

func TestIsValidSignedTransaction_StorageProofVerification(t *testing.T) {
	s := &Server{}

	marshal := func(t *testing.T, spv *v1.StorageProofVerification) []byte {
		t.Helper()
		return marshalSignedTx(t, &v1.SignedTransaction{
			Transaction: &v1.SignedTransaction_StorageProofVerification{StorageProofVerification: spv},
		})
	}

	t.Run("valid", func(t *testing.T) {
		tx := marshal(t, &v1.StorageProofVerification{Height: 100, Proof: []byte("proof-data")})
		msg, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
		require.Equal(t, int64(100), msg.GetStorageProofVerification().Height)
	})

	t.Run("zero height", func(t *testing.T) {
		tx := marshal(t, &v1.StorageProofVerification{Proof: []byte("proof-data")})
		_, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
		require.Equal(t, uint32(1), checkTxCode(t, tx))
	})

	t.Run("empty proof", func(t *testing.T) {
		tx := marshal(t, &v1.StorageProofVerification{Height: 100})
		_, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
		require.Equal(t, uint32(1), checkTxCode(t, tx))
	})
}

func TestIsValidSignedTransaction_Attestation(t *testing.T) {
	s := &Server{}

	marshal := func(t *testing.T, att *v1.Attestation) []byte {
		t.Helper()
		return marshalSignedTx(t, &v1.SignedTransaction{
			Transaction: &v1.SignedTransaction_Attestation{Attestation: att},
		})
	}

	t.Run("valid registration", func(t *testing.T) {
		tx := marshal(t, &v1.Attestation{
			Signatures: []string{"sig1", "sig2"},
			Body: &v1.Attestation_ValidatorRegistration{
				ValidatorRegistration: &v1.ValidatorRegistration{},
			},
		})
		_, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
	})

	t.Run("valid deregistration", func(t *testing.T) {
		tx := marshal(t, &v1.Attestation{
			Signatures: []string{"sig1"},
			Body: &v1.Attestation_ValidatorDeregistration{
				ValidatorDeregistration: &v1.ValidatorDeregistration{},
			},
		})
		_, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
	})

	t.Run("empty signatures pass structural validation", func(t *testing.T) {
		tx := marshal(t, &v1.Attestation{
			Body: &v1.Attestation_ValidatorRegistration{
				ValidatorRegistration: &v1.ValidatorRegistration{},
			},
		})
		_, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
	})

	t.Run("no body", func(t *testing.T) {
		tx := marshal(t, &v1.Attestation{
			Signatures: []string{"sig1"},
		})
		_, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
		require.Equal(t, uint32(1), checkTxCode(t, tx))
	})
}

func TestIsValidSignedTransaction_OtherTypes(t *testing.T) {
	s := &Server{}

	t.Run("plays passes through", func(t *testing.T) {
		tx := marshalSignedTx(t, &v1.SignedTransaction{
			Transaction: &v1.SignedTransaction_Plays{Plays: &v1.TrackPlays{}},
		})
		_, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
	})

	t.Run("manage entity passes through", func(t *testing.T) {
		tx := marshalSignedTx(t, &v1.SignedTransaction{
			Transaction: &v1.SignedTransaction_ManageEntity{ManageEntity: &v1.ManageEntityLegacy{}},
		})
		_, err := s.isValidSignedTransaction(tx)
		require.NoError(t, err)
	})
}
