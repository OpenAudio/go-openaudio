package rewards_test

import (
	"encoding/hex"
	"testing"

	"github.com/AudiusProject/audiusd/pkg/rewards"
	"github.com/stretchr/testify/require"
)

func TestCompile(t *testing.T) {
	// Test with anti abuse oracle
	claim := rewards.RewardClaim{
		RewardID:                  "c",
		Specifier:                 "b9256e3:202515",
		Amount:                    uint64(1),
		RecipientEthAddress:       "0xe811761771ef65f9de0b64d6335f3b8ff50adc44",
		AntiAbuseOracleEthAddress: "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3",
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
