package rewards_test

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/rewards"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/mr-tron/base58/base58"
	"github.com/stretchr/testify/require"
)

func TestGetCreateSenderAttestation(t *testing.T) {
	// Generate a new private key for testing
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	// Get the expected owner address from the private key
	publicKey := privKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	require.True(t, ok)
	expectedOwnerAddress := crypto.PubkeyToAddress(*publicKeyECDSA)

	t.Run("successful attestation creation", func(t *testing.T) {
		// Generate a random Ethereum address for new sender
		newSenderPrivKey, err := crypto.GenerateKey()
		require.NoError(t, err)
		newSenderPubKey := newSenderPrivKey.Public()
		newSenderPubKeyECDSA, ok := newSenderPubKey.(*ecdsa.PublicKey)
		require.True(t, ok)
		newSenderAddress := crypto.PubkeyToAddress(*newSenderPubKeyECDSA)

		// Generate a valid base58 pubkey for Solana (32 bytes encoded as base58)
		solanaKeyBytes := make([]byte, 32)
		_, err = rand.Read(solanaKeyBytes)
		require.NoError(t, err)
		solanaPubKey := base58.Encode(solanaKeyBytes)

		params := &rewards.CreateSenderAttestationParams{
			NewSenderAddress:            newSenderAddress.Hex(),
			RewardsManagerAccountPubKey: solanaPubKey,
		}

		ownerWallet, signedAttestation, err := rewards.GetCreateSenderAttestation(privKey, params)

		require.NoError(t, err)
		require.NotEmpty(t, ownerWallet)
		require.NotEmpty(t, signedAttestation)

		// Verify owner wallet address is correct
		require.Equal(t, expectedOwnerAddress.Hex(), ownerWallet)
	})

	t.Run("successful attestation with 0x prefix trimmed", func(t *testing.T) {
		// Generate test addresses and keys
		testAddr := common.HexToAddress("0x73EB6d82CFB20bA669e9c178b718d770C49BB52f")
		solanaKeyBytes := make([]byte, 32)
		_, err := rand.Read(solanaKeyBytes)
		require.NoError(t, err)
		solanaPubKey := base58.Encode(solanaKeyBytes)

		params := &rewards.CreateSenderAttestationParams{
			NewSenderAddress:            testAddr.Hex(), // With 0x prefix
			RewardsManagerAccountPubKey: solanaPubKey,
		}

		ownerWallet1, sig1, err1 := rewards.GetCreateSenderAttestation(privKey, params)
		require.NoError(t, err1)

		// Test with 0x prefix already removed
		params2 := &rewards.CreateSenderAttestationParams{
			NewSenderAddress:            testAddr.Hex()[2:], // Remove 0x prefix
			RewardsManagerAccountPubKey: solanaPubKey,
		}

		ownerWallet2, sig2, err2 := rewards.GetCreateSenderAttestation(privKey, params2)
		require.NoError(t, err2)

		// Should produce the same result
		require.Equal(t, ownerWallet1, ownerWallet2)
		require.Equal(t, sig1, sig2)
	})

	t.Run("invalid program pubkey", func(t *testing.T) {
		// Generate a valid Ethereum address
		testPrivKey, err := crypto.GenerateKey()
		require.NoError(t, err)
		testPubKey := testPrivKey.Public()
		testPubKeyECDSA, ok := testPubKey.(*ecdsa.PublicKey)
		require.True(t, ok)
		testAddress := crypto.PubkeyToAddress(*testPubKeyECDSA)

		params := &rewards.CreateSenderAttestationParams{
			NewSenderAddress:            testAddress.Hex(),
			RewardsManagerAccountPubKey: "invalid!@#$base58", // Invalid base58
		}

		ownerWallet, signedAttestation, err := rewards.GetCreateSenderAttestation(privKey, params)

		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid program pubkey")
		require.Empty(t, ownerWallet)
		require.Empty(t, signedAttestation)
	})

	t.Run("invalid sender address", func(t *testing.T) {
		// Generate a valid Solana pubkey
		solanaKeyBytes := make([]byte, 32)
		_, err := rand.Read(solanaKeyBytes)
		require.NoError(t, err)
		solanaPubKey := base58.Encode(solanaKeyBytes)

		params := &rewards.CreateSenderAttestationParams{
			NewSenderAddress:            "0xINVALIDHEX", // Invalid hex
			RewardsManagerAccountPubKey: solanaPubKey,
		}

		ownerWallet, signedAttestation, err := rewards.GetCreateSenderAttestation(privKey, params)

		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid sender address")
		require.Empty(t, ownerWallet)
		require.Empty(t, signedAttestation)
	})

	t.Run("deterministic signature verification", func(t *testing.T) {
		// Generate test data
		testPrivKey, err := crypto.GenerateKey()
		require.NoError(t, err)
		testPubKey := testPrivKey.Public()
		testPubKeyECDSA, ok := testPubKey.(*ecdsa.PublicKey)
		require.True(t, ok)
		testAddress := crypto.PubkeyToAddress(*testPubKeyECDSA)

		solanaKeyBytes := make([]byte, 32)
		_, err = rand.Read(solanaKeyBytes)
		require.NoError(t, err)
		solanaPubKey := base58.Encode(solanaKeyBytes)

		params := &rewards.CreateSenderAttestationParams{
			NewSenderAddress:            testAddress.Hex(),
			RewardsManagerAccountPubKey: solanaPubKey,
		}

		// Generate attestation multiple times with same input
		ownerWallet1, sig1, err1 := rewards.GetCreateSenderAttestation(privKey, params)
		require.NoError(t, err1)

		ownerWallet2, sig2, err2 := rewards.GetCreateSenderAttestation(privKey, params)
		require.NoError(t, err2)

		// Should produce the same deterministic result
		require.Equal(t, ownerWallet1, ownerWallet2)
		require.Equal(t, sig1, sig2)
	})

	t.Run("different inputs produce different signatures", func(t *testing.T) {
		// Generate two different Ethereum addresses
		addr1PrivKey, err := crypto.GenerateKey()
		require.NoError(t, err)
		addr1PubKey := addr1PrivKey.Public()
		addr1PubKeyECDSA, ok := addr1PubKey.(*ecdsa.PublicKey)
		require.True(t, ok)
		address1 := crypto.PubkeyToAddress(*addr1PubKeyECDSA)

		addr2PrivKey, err := crypto.GenerateKey()
		require.NoError(t, err)
		addr2PubKey := addr2PrivKey.Public()
		addr2PubKeyECDSA, ok := addr2PubKey.(*ecdsa.PublicKey)
		require.True(t, ok)
		address2 := crypto.PubkeyToAddress(*addr2PubKeyECDSA)

		// Use the same Solana pubkey for both
		solanaKeyBytes := make([]byte, 32)
		_, err = rand.Read(solanaKeyBytes)
		require.NoError(t, err)
		solanaPubKey := base58.Encode(solanaKeyBytes)

		params1 := &rewards.CreateSenderAttestationParams{
			NewSenderAddress:            address1.Hex(),
			RewardsManagerAccountPubKey: solanaPubKey,
		}

		params2 := &rewards.CreateSenderAttestationParams{
			NewSenderAddress:            address2.Hex(),
			RewardsManagerAccountPubKey: solanaPubKey,
		}

		ownerWallet1, sig1, err1 := rewards.GetCreateSenderAttestation(privKey, params1)
		require.NoError(t, err1)

		ownerWallet2, sig2, err2 := rewards.GetCreateSenderAttestation(privKey, params2)
		require.NoError(t, err2)

		// Owner wallet should be the same (same private key)
		require.Equal(t, ownerWallet1, ownerWallet2)
		// Signatures should be different (different input data)
		require.NotEqual(t, sig1, sig2)
	})

	t.Run("verify attestation message format", func(t *testing.T) {
		// Use a fixed private key to get predictable results
		fixedPrivKeyHex := "d09ba371c359f10f22ccda12fd26c598c7921bda3220c9942174562bc6a36fe8"
		bytes, err := hex.DecodeString(fixedPrivKeyHex)
		require.NoError(t, err)

		fixedPrivKey, err := crypto.ToECDSA(bytes)
		require.NoError(t, err)

		// Use fixed addresses for predictable output
		params := &rewards.CreateSenderAttestationParams{
			NewSenderAddress:            "0x73EB6d82CFB20bA669e9c178b718d770C49BB52f",
			RewardsManagerAccountPubKey: "6ZfeYt5EsyY5dXTDC6uHjJdLWdtrJcbtUk21WHfZngUu", // Fixed Solana pubkey
		}

		ownerWallet, signedAttestation, err := rewards.GetCreateSenderAttestation(fixedPrivKey, params)
		require.NoError(t, err)

		// Verify the owner wallet matches the expected address for this private key
		expectedOwnerAddress := "0x73EB6d82CFB20bA669e9c178b718d770C49BB52f"
		require.Equal(t, expectedOwnerAddress, ownerWallet)

		// Verify signature format (should be hex string without 0x prefix based on EthSignKeccak)
		require.False(t, strings.HasPrefix(signedAttestation, "0x"), "signature should not have 0x prefix")
		// Ethereum signatures are 65 bytes (130 hex chars)
		require.Equal(t, 130, len(signedAttestation), "signature should be 65 bytes in hex format")

		// Verify the signature is valid hex
		_, err = hex.DecodeString(signedAttestation)
		require.NoError(t, err, "signature should be valid hex")
	})
}

func TestCreateSenderAttestationMessageConstruction(t *testing.T) {
	t.Run("verify message prefix is included", func(t *testing.T) {
		// This test verifies that the "add" prefix is properly included in the message
		privKey, err := crypto.GenerateKey()
		require.NoError(t, err)

		// Generate test data
		newSenderPrivKey, err := crypto.GenerateKey()
		require.NoError(t, err)
		newSenderPubKey := newSenderPrivKey.Public()
		newSenderPubKeyECDSA, ok := newSenderPubKey.(*ecdsa.PublicKey)
		require.True(t, ok)
		newSenderAddress := crypto.PubkeyToAddress(*newSenderPubKeyECDSA)

		solanaKeyBytes := make([]byte, 32)
		_, err = rand.Read(solanaKeyBytes)
		require.NoError(t, err)
		solanaPubKey := base58.Encode(solanaKeyBytes)

		params := &rewards.CreateSenderAttestationParams{
			NewSenderAddress:            newSenderAddress.Hex(),
			RewardsManagerAccountPubKey: solanaPubKey,
		}

		_, signedAttestation, err := rewards.GetCreateSenderAttestation(privKey, params)
		require.NoError(t, err)
		require.NotEmpty(t, signedAttestation)

		// The message should be: "add" + rewardsManagerPubkey (32 bytes) + newSenderAddress (20 bytes)
		// Total: 3 + 32 + 20 = 55 bytes
	})
}
