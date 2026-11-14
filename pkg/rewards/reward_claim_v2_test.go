package rewards

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func fixtureV2() RewardClaimV2 {
	var c RewardClaimV2
	for i := range c.ChainDomain {
		c.ChainDomain[i] = 1
		c.ProgramID[i] = 2
		c.RewardManager[i] = 3
		c.Mint[i] = 4
	}
	for i := range c.RewardAddress {
		c.RewardAddress[i] = 5
		c.Recipient[i] = 6
		c.Oracle[i] = 7
	}
	c.Amount = 2325000000000000
	c.Epoch = 1
	c.ValidUntilSlot = 500000000
	c.DisbursementID = "reward:nonce"
	return c
}

func TestClaimV2Golden(t *testing.T) {
	c := fixtureV2()
	for _, role := range []ClaimRole{ClaimRoleOracle, ClaimRoleValidator} {
		message, err := c.Compile(role)
		require.NoError(t, err)
		require.Len(t, message, 52)
		expected := "417564697573526577617264436c61696d56320119f09a18a91318a4fb4ea4fab4957998682a5ea154eb4e5ac8e5c5b1a02e15c5"
		if role == ClaimRoleValidator {
			expected = "417564697573526577617264436c61696d56320219f09a18a91318a4fb4ea4fab4957998682a5ea154eb4e5ac8e5c5b1a02e15c5"
		}
		require.Equal(t, expected, hex.EncodeToString(message))
	}
}

func TestClaimV2SignatureBindsEveryField(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	claim := fixtureV2()
	claim.Oracle = crypto.PubkeyToAddress(key.PublicKey)
	signature, err := SignClaimV2(claim, ClaimRoleOracle, key)
	require.NoError(t, err)
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, "0x"))
	require.NoError(t, err)
	require.NoError(t, VerifyOracleV2(claim, sig))
	mutations := map[string]func(*RewardClaimV2){
		"chain":     func(c *RewardClaimV2) { c.ChainDomain[0] ^= 1 },
		"program":   func(c *RewardClaimV2) { c.ProgramID[0] ^= 1 },
		"pool":      func(c *RewardClaimV2) { c.RewardManager[0] ^= 1 },
		"mint":      func(c *RewardClaimV2) { c.Mint[0] ^= 1 },
		"reward":    func(c *RewardClaimV2) { c.RewardAddress[0] ^= 1 },
		"recipient": func(c *RewardClaimV2) { c.Recipient[0] ^= 1 },
		"oracle":    func(c *RewardClaimV2) { c.Oracle[0] ^= 1 },
		"amount":    func(c *RewardClaimV2) { c.Amount++ },
		"epoch":     func(c *RewardClaimV2) { c.Epoch++ },
		"expiry":    func(c *RewardClaimV2) { c.ValidUntilSlot++ },
		"nonce":     func(c *RewardClaimV2) { c.DisbursementID += "_" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) { changed := claim; mutate(&changed); require.Error(t, VerifyOracleV2(changed, sig)) })
	}
	validatorSig, err := SignClaimV2(claim, ClaimRoleValidator, key)
	require.NoError(t, err)
	raw, _ := hex.DecodeString(strings.TrimPrefix(validatorSig, "0x"))
	require.Error(t, VerifyOracleV2(claim, raw), "validator signature must not serve as oracle")
	otherKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	_, err = SignClaimV2(claim, ClaimRoleOracle, otherKey)
	require.Error(t, err, "oracle field must match signing key")
}

func TestClaimV2RejectsLegacyRoleCollision(t *testing.T) {
	validator := fixtureV2()
	validator.DisbursementID = "reward:d3"
	for i := range validator.Oracle {
		validator.Oracle[i] = 0x41
	}
	oracle := validator
	oracle.DisbursementID = "reward:d3_AAAAAAAAAAAAAAAAAAAA"
	v, err := validator.Compile(ClaimRoleValidator)
	require.NoError(t, err)
	o, err := oracle.Compile(ClaimRoleOracle)
	require.NoError(t, err)
	require.NotEqual(t, v, o)
}

func TestClaimV2RejectsInvalidContext(t *testing.T) {
	mutations := []func(*RewardClaimV2){
		func(c *RewardClaimV2) { c.Oracle = [20]byte{} }, func(c *RewardClaimV2) { c.RewardManager = [32]byte{} },
		func(c *RewardClaimV2) { c.ChainDomain = [32]byte{} }, func(c *RewardClaimV2) { c.Amount = 0 },
		func(c *RewardClaimV2) { c.Epoch = 0 }, func(c *RewardClaimV2) { c.ValidUntilSlot = 0 },
		func(c *RewardClaimV2) { c.DisbursementID = "" }, func(c *RewardClaimV2) { c.DisbursementID = strings.Repeat("x", 31) },
		func(c *RewardClaimV2) { c.DisbursementID = string([]byte{0xff}) },
	}
	for _, mutate := range mutations {
		c := fixtureV2()
		mutate(&c)
		_, err := c.Compile(ClaimRoleOracle)
		require.Error(t, err)
	}
	_, err := fixtureV2().Compile(0)
	require.Error(t, err)
}
