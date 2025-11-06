package rewards_test

import (
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/rewards"
	"github.com/stretchr/testify/require"
)

func TestCompile(t *testing.T) {
	// Test with anti abuse oracle
	claim := rewards.RewardClaim{
		RewardID:            "c",
		Specifier:           "b9256e3:202515",
		Amount:              uint64(1),
		RecipientEthAddress: "0xe811761771ef65f9de0b64d6335f3b8ff50adc44",
		ClaimAuthority:      "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3",
	}

	expectedHex := "e811761771ef65f9de0b64d6335f3b8ff50adc445f00e1f505000000005f633a623932353665333a3230323531355ff0d5bc18421fa04d0a2a2ef540ba5a9f04014be3"

	expected, err := hex.DecodeString(expectedHex)
	require.NoError(t, err)

	asBytes, err := claim.Compile()
	require.NoError(t, err)
	require.Equal(t, expected, asBytes, "should compile with anti abuse oracle address")

	// Test w/o anti abuse oracle
	claim = rewards.RewardClaim{
		RewardID:            "c",
		Specifier:           "b9256e3:202515",
		Amount:              uint64(1),
		RecipientEthAddress: "0xe811761771ef65f9de0b64d6335f3b8ff50adc44",
	}

	expectedHex = "e811761771ef65f9de0b64d6335f3b8ff50adc445f00e1f505000000005f633a623932353665333a323032353135"

	expected, err = hex.DecodeString(expectedHex)
	require.NoError(t, err)

	asBytes, err = claim.Compile()
	require.NoError(t, err)
	require.Equal(t, expected, asBytes, "should compile without anti abuse oracle address")
}

func TestCompileWithDecimals(t *testing.T) {
	// Base claim values
	baseClaim := rewards.RewardClaim{
		RewardID:            "c",
		Specifier:           "b9256e3:202515",
		Amount:              uint64(1),
		RecipientEthAddress: "0xe811761771ef65f9de0b64d6335f3b8ff50adc44",
	}

	// Helper to extract the 8 amount bytes:
	// [0:20] = user (20 bytes), [20] = '_', [21:29] = amount (8 bytes)
	getAmountBytes := func(b []byte) []byte {
		const off = 20 + 1
		if len(b) < off+8 {
			t.Fatalf("compiled bytes too short: got %d", len(b))
		}
		return b[off : off+8]
	}

	// Decimals = 8 (default), expect 1 * 10^8
	c8 := baseClaim
	c8.Decimals = 8
	out8, err := c8.Compile()
	require.NoError(t, err)
	amt8 := getAmountBytes(out8)
	expected8 := make([]byte, 8)
	binary.LittleEndian.PutUint64(expected8, 100_000_000)
	require.Equal(t, expected8, amt8)

	// Decimals = 6, expect 1 * 10^6
	c6 := baseClaim
	c6.Decimals = 6
	out6, err := c6.Compile()
	require.NoError(t, err)
	amt6 := getAmountBytes(out6)
	expected6 := make([]byte, 8)
	binary.LittleEndian.PutUint64(expected6, 1_000_000)
	require.Equal(t, expected6, amt6)

	// Decimals too large should error
	cBad := baseClaim
	cBad.Decimals = 19
	_, err = cBad.Compile()
	require.Error(t, err)
}
