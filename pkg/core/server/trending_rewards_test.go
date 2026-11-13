package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/rewards"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestStaticTrendingAttestationRemainsAvailable(t *testing.T) {
	authority, err := crypto.GenerateKey()
	require.NoError(t, err)
	validator, err := crypto.GenerateKey()
	require.NoError(t, err)
	configured := config.MakeRewards([]rewards.ClaimAuthority{{Address: crypto.PubkeyToAddress(authority.PublicKey).Hex()}}, nil)
	server := &Server{rewards: rewards.NewRewardAttester(validator, configured)}
	for _, id := range []string{"tt", "tut"} {
		for _, amount := range []uint64{1000, 100} {
			specifier := "2026-11-13:1"
			if amount == 100 {
				specifier = "2026-11-13:6"
			}
			claim := rewards.RewardClaim{RewardID: id, Specifier: specifier, Amount: amount,
				RecipientEthAddress: "0x1111111111111111111111111111111111111111", ClaimAuthority: "0x2222222222222222222222222222222222222222"}
			signature, err := rewards.SignClaim(claim, authority)
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodGet, "/core/rewards/attestation", nil)
			q := req.URL.Query()
			q.Set("reward_id", id)
			q.Set("specifier", specifier)
			q.Set("amount", strconv.FormatUint(amount, 10))
			q.Set("eth_recipient_address", claim.RecipientEthAddress)
			q.Set("oracle_address", claim.ClaimAuthority)
			q.Set("signature", signature)
			req.URL.RawQuery = q.Encode()
			recorder := httptest.NewRecorder()
			require.NoError(t, server.getRewardAttestation(echo.New().NewContext(req, recorder)))
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			var response struct {
				Attestation string `json:"attestation"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			signer, err := rewards.VerifyClaim(claim, response.Attestation)
			require.NoError(t, err)
			require.Equal(t, crypto.PubkeyToAddress(validator.PublicKey).Hex(), signer)
		}
	}
	for _, id := range []string{"e", "p", "tp", "dvl", "rbig1"} {
		require.Error(t, server.rewards.Validate(rewards.RewardClaim{RewardID: id, Amount: 1}))
	}
}
