package rewards

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

func (rs *RewardService) GetRewards(c echo.Context) error {
	return c.JSON(http.StatusOK, rs.Rewards)
}

func (rs *RewardService) AttestReward(c echo.Context) error {
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

	reward, err := rs.GetRewardById(rewardID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, err.Error())
	}

	if amountUint != reward.Amount {
		return c.JSON(http.StatusBadRequest, "amount does not match reward amount")
	}

	claimDataHash, err := GetClaimDataHash(ethRecipientAddress, rewardID, specifier, oracleAddress, amountUint)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, err.Error())
	}

	// recreate hash with registered reward data
	expectedClaimDataHash, err := GetClaimDataHash(ethRecipientAddress, reward.RewardId, specifier, oracleAddress, reward.Amount)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, err.Error())
	}

	if !bytes.Equal(claimDataHash, expectedClaimDataHash) {
		return c.JSON(http.StatusBadRequest, "claim data hash does not match expected claim data hash")
	}

	recoveredWallet, err := RecoverWalletFromSignature(claimDataHash, signature)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, err.Error())
	}

	if !ValidClaimAuthority(reward.ClaimAuthorities, recoveredWallet) {
		return c.JSON(http.StatusUnauthorized, fmt.Sprintf("wallet %s is not authorized to claim reward %s", recoveredWallet, rewardID))
	}

	// construct attestation bytes
	attestationBytes, err := GetAttestationBytes(ethRecipientAddress, rewardID, specifier, oracleAddress, reward.Amount)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, err.Error())
	}

	owner, attestation, err := rs.SignAttestation(attestationBytes)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, err.Error())
	}

	res := map[string]any{
		"owner":       owner,
		"attestation": attestation,
	}
	return c.JSON(http.StatusOK, res)
}
