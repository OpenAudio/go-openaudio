package server

import (
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/rewards"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/labstack/echo/v4"
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

func TestGetRewardAttestation(t *testing.T) {
	// arbitrary privkey
	privKeyHex := "3b44ad90f0a3bf1c00b3b960c1b575db4d65a3e0b883f47ff80e3323b1c1c2f2"

	bytes, err := hex.DecodeString(privKeyHex)
	if err != nil {
		log.Fatalf("failed to decode hex: %v", err)
	}

	privKey, err := crypto.ToECDSA(bytes)
	if err != nil {
		log.Fatalf("failed to convert to ECDSA: %v", err)
	}

	// Setup
	e := echo.New()
	attester := rewards.NewRewardAttester(privKey, TestRewards)
	s := &Server{rewards: attester}

	if err != nil {
		log.Fatalf("couldn't create server for test: %s", err)
	}

	// Test data
	rewardID := "c"
	specifier := "b9256e3:202515"
	amount := "1"
	ethRecipientAddress := "0xe811761771ef65f9de0b64d6335f3b8ff50adc44"
	oracleAddress := "0xF0D5BC18421fa04D0a2A2ef540ba5A9f04014BE3"
	signature := "0x661327f5968ac95063dff94dcedbcfcf8dd464461aceffba5071dcf05b3287dc3dd69d86ba3b8776ad4b7e2116c71e148938a539403975ae8439b2acdd93348901"

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	q := req.URL.Query()
	q.Add("reward_id", rewardID)
	q.Add("specifier", specifier)
	q.Add("eth_recipient_address", ethRecipientAddress)
	q.Add("oracle_address", oracleAddress)
	q.Add("signature", signature)
	q.Add("amount", amount)
	req.URL.RawQuery = q.Encode()

	// Create recorder and context
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Call handler
	err = s.getRewardAttestation(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	expectedAttestation := "0xf1641b660c0bf4374e0de4af0ae17726d87a04edcc8aa20d6ca28aee83e233651d42f4d0198f6c8c5a5fb8b562016d38cea206fb5f26987cb1049da9180621fc00"
	expectedOwner := "0x5f4fdaf7dF2589ff84fD50969Ed415c2F3F7CA8D"

	// Parse response
	var res map[string]any
	err = json.Unmarshal(rec.Body.Bytes(), &res)
	require.NoError(t, err)

	// Assertions
	require.Equal(t, expectedAttestation, res["attestation"])
	require.Equal(t, expectedOwner, res["owner"])
}
