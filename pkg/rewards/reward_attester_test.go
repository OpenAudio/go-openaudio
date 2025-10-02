package rewards_test

import (
	"encoding/hex"
	"log"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/rewards"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

var (
	TestRewards = []rewards.Reward{
		{
			Amount:           1,
			RewardId:         "c",
			Name:             "first weekly comment",
			ClaimAuthorities: []rewards.ClaimAuthority{{Address: "0x73EB6d82CFB20bA669e9c178b718d770C49BB52f", Name: "TikiLabsDiscovery"}},
		},
	}
)

func TestValidate(t *testing.T) {
	attester := rewards.NewRewardAttester(nil, TestRewards)
	claim := rewards.RewardClaim{
		RewardID:                  "xxx",
		Specifier:                 "b9256e3:202515",
		Amount:                    uint64(1),
		RecipientEthAddress:       "0xe811761771ef65f9de0b64d6335f3b8ff50adc44",
		AntiAbuseOracleEthAddress: "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3",
	}
	err := attester.Validate((claim))
	require.Error(t, err, "should error if challenge ID isn't configured")

	claim = rewards.RewardClaim{
		RewardID:                  "c",
		Amount:                    uint64(1),
		RecipientEthAddress:       "0xe811761771ef65f9de0b64d6335f3b8ff50adc44",
		AntiAbuseOracleEthAddress: "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3",
	}
	err = attester.Validate((claim))
	require.Error(t, err, "should error if specifier is missing")

	claim = rewards.RewardClaim{
		RewardID:                  "c",
		Specifier:                 "b9256e3:202515",
		RecipientEthAddress:       "0xe811761771ef65f9de0b64d6335f3b8ff50adc44",
		AntiAbuseOracleEthAddress: "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3",
	}
	err = attester.Validate((claim))
	require.Error(t, err, "should error if amount is missing")

	claim = rewards.RewardClaim{
		RewardID:                  "c",
		Specifier:                 "b9256e3:202515",
		Amount:                    uint64(10000),
		AntiAbuseOracleEthAddress: "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3",
	}
	err = attester.Validate(claim)
	require.Error(t, err, "should error if recipient eth address is missing")

	claim = rewards.RewardClaim{
		RewardID:                  "c",
		Specifier:                 "b9256e3:202515",
		Amount:                    uint64(10000),
		RecipientEthAddress:       "0xe811761771ef65f9de0b64d6335f3b8ff50adc44",
		AntiAbuseOracleEthAddress: "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3",
	}
	err = attester.Validate(claim)
	require.Error(t, err, "should error if amount doesn't match config")
}

func TestAuthenticate(t *testing.T) {
	claim := rewards.RewardClaim{
		RewardID:                  "c",
		Specifier:                 "b9256e3:202515",
		Amount:                    uint64(1),
		RecipientEthAddress:       "0xe811761771ef65f9de0b64d6335f3b8ff50adc44",
		AntiAbuseOracleEthAddress: "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3",
	}
	signature := "0x661327f5968ac95063dff94dcedbcfcf8dd464461aceffba5071dcf05b3287dc3dd69d86ba3b8776ad4b7e2116c71e148938a539403975ae8439b2acdd93348901"

	// Explicitly don't set up the authorities
	attester := rewards.NewRewardAttester(nil, []rewards.Reward{{RewardId: "c", Amount: uint64(1), ClaimAuthorities: []rewards.ClaimAuthority{}}})

	err := attester.Authenticate(claim, signature)
	require.Error(t, err, "should error when signed by unauthorized signer")

	// Add the address to the claim authorities
	for i := range attester.Rewards {
		attester.Rewards[i].ClaimAuthorities = []rewards.ClaimAuthority{{Address: "0x73EB6d82CFB20bA669e9c178b718d770C49BB52f"}}
	}

	// Now authenticate
	err = attester.Authenticate(claim, signature)
	require.NoError(t, err, "should not error when signed by authorized signer")
}

func TestAttest(t *testing.T) {
	privKeyHex := "d09ba371c359f10f22ccda12fd26c598c7921bda3220c9942174562bc6a36fe8"

	bytes, err := hex.DecodeString(privKeyHex)
	if err != nil {
		log.Fatalf("failed to decode hex: %v", err)
	}

	privKey, err := crypto.ToECDSA(bytes)
	if err != nil {
		log.Fatalf("failed to convert to ECDSA: %v", err)
	}

	attester := rewards.NewRewardAttester(privKey, TestRewards)

	claim := rewards.RewardClaim{
		RewardID:                  "c",
		Specifier:                 "b9256e3:202515",
		Amount:                    uint64(1),
		RecipientEthAddress:       "0xe811761771ef65f9de0b64d6335f3b8ff50adc44",
		AntiAbuseOracleEthAddress: "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3",
	}
	message, signature, err := attester.Attest(claim)

	expectedMessage := []byte{232, 17, 118, 23, 113, 239, 101, 249, 222, 11, 100, 214, 51, 95, 59, 143, 245, 10, 220, 68, 95, 0, 225, 245, 5, 0, 0, 0, 0, 95, 99, 58, 98, 57, 50, 53, 54, 101, 51, 58, 50, 48, 50, 53, 49, 53, 95, 240, 213, 188, 24, 66, 31, 160, 77, 10, 42, 46, 245, 64, 186, 90, 159, 4, 1, 75, 227}
	expectedSignature := "0x661327f5968ac95063dff94dcedbcfcf8dd464461aceffba5071dcf05b3287dc3dd69d86ba3b8776ad4b7e2116c71e148938a539403975ae8439b2acdd93348901"

	require.Equal(t, expectedMessage, message)
	require.Equal(t, expectedSignature, signature)

	require.NoError(t, err)
}
