package server

import (
	"net/http"
	"strconv"

	"github.com/AudiusProject/audiusd/pkg/rewards"
	"github.com/labstack/echo/v4"
)

func (s *Server) getRewards(c echo.Context) error {
	return c.JSON(http.StatusOK, s.rewards.Rewards)
}

func (s *Server) getRewardAttestation(c echo.Context) error {
	ethRecipientAddress := c.QueryParam("eth_recipient_address")
	if ethRecipientAddress == "" {
		return c.JSON(http.StatusBadRequest, "eth_recipient_address is required")
	}
	rewardID := c.QueryParam("reward_id")
	if rewardID == "" {
		return c.JSON(http.StatusBadRequest, "reward_id is required")
	}
	specifier := c.QueryParam("specifier")
	if specifier == "" {
		return c.JSON(http.StatusBadRequest, "specifier is required")
	}
	oracleAddress := c.QueryParam("oracle_address")
	if oracleAddress == "" {
		return c.JSON(http.StatusBadRequest, "oracle_address is required")
	}
	signature := c.QueryParam("signature")
	if signature == "" {
		return c.JSON(http.StatusBadRequest, "signature is required")
	}
	amount := c.QueryParam("amount")
	if amount == "" {
		return c.JSON(http.StatusBadRequest, "amount is required")
	}
	amountUint, err := strconv.ParseUint(amount, 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, "amount is invalid")
	}

	claim := rewards.RewardClaim{
		RecipientEthAddress:       ethRecipientAddress,
		Amount:                    amountUint,
		RewardID:                  rewardID,
		Specifier:                 specifier,
		AntiAbuseOracleEthAddress: oracleAddress,
	}

	err = s.rewards.Validate(claim)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}

	err = s.rewards.Authenticate(claim, signature)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, err.Error())
	}

	_, attestation, err := s.rewards.Attest(claim)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, err.Error())
	}

	res := map[string]any{
		"owner":       s.rewards.EthereumAddress,
		"attestation": attestation,
	}
	return c.JSON(http.StatusOK, res)
}
