package rewards_test

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AudiusProject/audiusd/pkg/core/config"
	"github.com/AudiusProject/audiusd/pkg/core/rewards"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

var (
	privKey = mustPrivateKeyFromHex("d09ba371c359f10f22ccda12fd26c598c7921bda3220c9942174562bc6a36fe8")
)

func mustPrivateKeyFromHex(hexKey string) *ecdsa.PrivateKey {
	bytes, err := hex.DecodeString(hexKey)
	if err != nil {
		log.Fatalf("failed to decode hex: %v", err)
	}
	privKey, err := crypto.ToECDSA(bytes)
	if err != nil {
		log.Fatalf("failed to convert to ECDSA: %v", err)
	}
	return privKey
}

func testSignature() string {
	rewardID := "c"
	specifier := "b9256e3:202515"
	userWallet := "0xe811761771ef65f9de0b64d6335f3b8ff50adc44"
	oracleAddress := "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3"
	claimDataHash, err := rewards.GetClaimDataHash(userWallet, rewardID, specifier, oracleAddress, 1)
	if err != nil {
		log.Fatalf("failed to get claim data hash: %v", err)
	}
	signature, err := rewards.SignClaimDataHash(claimDataHash, privKey)
	if err != nil {
		log.Fatalf("failed to sign claim data hash: %v", err)
	}
	return signature
}

func TestAttestRewardWithEcho(t *testing.T) {
	// curl "https://node1.audiusd.devnet/core/rewards/attest?reward_id=c&specifier=b9256e3:202515&user_wallet=0xe811761771ef65f9de0b64d6335f3b8ff50adc44&oracle_address=0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3&signature=0xe2a5877f389aecf80fdbf0467f37f94b8a7f14e7d76354ae8d7df2f0677f04d850eaca83a5726bd02db972fd46778870e872d9248ac23399aa726bcac42ab43301"

	// Setup
	e := echo.New()
	rs := rewards.NewRewardService(&config.Config{
		Environment: "dev",
		EthereumKey: privKey,
	})

	// Test data
	rewardID := "c"
	specifier := "b9256e3:202515"
	ethRecipientAddress := "0xe811761771ef65f9de0b64d6335f3b8ff50adc44"
	oracleAddress := "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3"
	signature := testSignature()

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	q := req.URL.Query()
	q.Add("reward_id", rewardID)
	q.Add("specifier", specifier)
	q.Add("eth_recipient_address", ethRecipientAddress)
	q.Add("oracle_address", oracleAddress)
	q.Add("signature", signature)
	q.Add("amount", "1")
	req.URL.RawQuery = q.Encode()

	// Create recorder and context
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Call handler
	err := rs.AttestReward(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	expectedAttestation := "0x661327f5968ac95063dff94dcedbcfcf8dd464461aceffba5071dcf05b3287dc3dd69d86ba3b8776ad4b7e2116c71e148938a539403975ae8439b2acdd93348901"
	expectedOwner := "0x73EB6d82CFB20bA669e9c178b718d770C49BB52f"

	// Parse response
	var res map[string]any
	err = json.Unmarshal(rec.Body.Bytes(), &res)
	require.NoError(t, err)

	// Assertions
	require.Equal(t, expectedAttestation, res["attestation"])
	require.Equal(t, expectedOwner, res["owner"])
}

func TestMissingParams(t *testing.T) {
	// Setup
	e := echo.New()
	rs := rewards.NewRewardService(&config.Config{
		Environment: "dev",
		EthereumKey: privKey,
	})

	// Test data
	rewardID := "c"
	specifier := "b9256e3:202515"
	ethRecipientAddress := "0xe811761771ef65f9de0b64d6335f3b8ff50adc44"
	oracleAddress := "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3"
	signature := testSignature()
	amount := "1"

	// Test cases for each missing parameter
	testCases := []struct {
		name         string
		missingParam string
		params       map[string]string
	}{
		{
			name:         "missing eth_recipient_address",
			missingParam: "eth_recipient_address",
			params: map[string]string{
				"reward_id":      rewardID,
				"specifier":      specifier,
				"oracle_address": oracleAddress,
				"signature":      signature,
				"amount":         amount,
			},
		},
		{
			name:         "missing reward_id",
			missingParam: "reward_id",
			params: map[string]string{
				"eth_recipient_address": ethRecipientAddress,
				"specifier":             specifier,
				"oracle_address":        oracleAddress,
				"signature":             signature,
				"amount":                amount,
			},
		},
		{
			name:         "missing specifier",
			missingParam: "specifier",
			params: map[string]string{
				"eth_recipient_address": ethRecipientAddress,
				"reward_id":             rewardID,
				"oracle_address":        oracleAddress,
				"signature":             signature,
				"amount":                amount,
			},
		},
		{
			name:         "missing oracle_address",
			missingParam: "oracle_address",
			params: map[string]string{
				"eth_recipient_address": ethRecipientAddress,
				"reward_id":             rewardID,
				"specifier":             specifier,
				"signature":             signature,
				"amount":                amount,
			},
		},
		{
			name:         "missing signature",
			missingParam: "signature",
			params: map[string]string{
				"eth_recipient_address": ethRecipientAddress,
				"reward_id":             rewardID,
				"specifier":             specifier,
				"oracle_address":        oracleAddress,
				"amount":                amount,
			},
		},
		{
			name:         "missing amount",
			missingParam: "amount",
			params: map[string]string{
				"eth_recipient_address": ethRecipientAddress,
				"reward_id":             rewardID,
				"specifier":             specifier,
				"oracle_address":        oracleAddress,
				"signature":             signature,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create request
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			q := req.URL.Query()
			for k, v := range tc.params {
				q.Add(k, v)
			}
			req.URL.RawQuery = q.Encode()

			// Create recorder and context
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			// Call handler
			err := rs.AttestReward(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusBadRequest, rec.Code)

			// Parse response
			var res string
			err = json.Unmarshal(rec.Body.Bytes(), &res)
			require.NoError(t, err)
			require.Contains(t, res, tc.missingParam)
		})
	}
}

func TestInvalidClaimHash(t *testing.T) {
	// Setup
	e := echo.New()
	rs := rewards.NewRewardService(&config.Config{
		Environment: "dev",
		EthereumKey: privKey,
	})

	// Test data
	rewardID := "c"
	specifier := "b9256e3:202515"
	ethRecipientAddress := "0xe811761771ef65f9de0b64d6335f3b8ff50adc44"
	oracleAddress := "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3"

	// Generate a signature for incorrect data
	incorrectData := "wrong_data"
	incorrectHash := crypto.Keccak256([]byte(incorrectData))
	signature, err := rewards.SignClaimDataHash(incorrectHash, privKey)
	require.NoError(t, err)

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	q := req.URL.Query()
	q.Add("reward_id", rewardID)
	q.Add("specifier", specifier)
	q.Add("eth_recipient_address", ethRecipientAddress)
	q.Add("oracle_address", oracleAddress)
	q.Add("signature", signature)
	q.Add("amount", "1")
	req.URL.RawQuery = q.Encode()

	// Create recorder and context
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Call handler
	err = rs.AttestReward(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// Parse response
	var res string
	err = json.Unmarshal(rec.Body.Bytes(), &res)
	require.NoError(t, err)
	require.Contains(t, res, "is not authorized")
}

func TestInvalidClaimSignature(t *testing.T) {
	// Setup
	e := echo.New()
	rs := rewards.NewRewardService(&config.Config{
		Environment: "dev",
		EthereumKey: privKey,
	})

	// Test data
	rewardID := "c"
	specifier := "b9256e3:202515"
	ethRecipientAddress := "0xe811761771ef65f9de0b64d6335f3b8ff50adc44"
	oracleAddress := "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3"

	// Generate a random private key
	randomPrivKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	// Generate signature with correct data but wrong private key
	claimDataHash, err := rewards.GetClaimDataHash(ethRecipientAddress, rewardID, specifier, oracleAddress, 1)
	require.NoError(t, err)
	signature, err := rewards.SignClaimDataHash(claimDataHash, randomPrivKey)
	require.NoError(t, err)

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	q := req.URL.Query()
	q.Add("reward_id", rewardID)
	q.Add("specifier", specifier)
	q.Add("eth_recipient_address", ethRecipientAddress)
	q.Add("oracle_address", oracleAddress)
	q.Add("signature", signature)
	q.Add("amount", "1")
	req.URL.RawQuery = q.Encode()

	// Create recorder and context
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Call handler
	err = rs.AttestReward(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// Parse response
	var res string
	err = json.Unmarshal(rec.Body.Bytes(), &res)
	require.NoError(t, err)
	require.Contains(t, res, "is not authorized")
}

func TestGenerateClaimSignature(t *testing.T) {
	rewardID := "c"
	specifier := "b9256e3:202515"
	ethRecipientAddress := "0xe811761771ef65f9de0b64d6335f3b8ff50adc44"
	oracleAddress := "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3"
	claimDataHash, err := rewards.GetClaimDataHash(ethRecipientAddress, rewardID, specifier, oracleAddress, 1)
	require.NoError(t, err)
	signature, err := rewards.SignClaimDataHash(claimDataHash, privKey)
	require.NoError(t, err)
	require.NotEmpty(t, signature)
}

func TestAmountMismatch(t *testing.T) {
	// Setup
	e := echo.New()
	rs := rewards.NewRewardService(&config.Config{
		Environment: "dev",
		EthereumKey: privKey,
	})

	// Test data
	rewardID := "c"
	specifier := "b9256e3:202515"
	ethRecipientAddress := "0xe811761771ef65f9de0b64d6335f3b8ff50adc44"
	oracleAddress := "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3"
	signature := testSignature()

	// Create request with incorrect amount
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	q := req.URL.Query()
	q.Add("reward_id", rewardID)
	q.Add("specifier", specifier)
	q.Add("eth_recipient_address", ethRecipientAddress)
	q.Add("oracle_address", oracleAddress)
	q.Add("signature", signature)
	q.Add("amount", "2") // Incorrect amount
	req.URL.RawQuery = q.Encode()

	// Create recorder and context
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Call handler
	err := rs.AttestReward(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// Parse response
	var res string
	err = json.Unmarshal(rec.Body.Bytes(), &res)
	require.NoError(t, err)
	require.Contains(t, res, "amount does not match reward amount")
}
