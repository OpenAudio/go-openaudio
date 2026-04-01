package entity_manager

import (
	"context"
	"strings"
)

type dashboardWalletCreateHandler struct{}

func (h *dashboardWalletCreateHandler) EntityType() string { return EntityTypeDashboardWalletUser }
func (h *dashboardWalletCreateHandler) Action() string     { return ActionCreate }

func (h *dashboardWalletCreateHandler) Handle(ctx context.Context, params *Params) error {
	wallet := strings.ToLower(params.MetadataString("wallet"))
	if wallet == "" {
		return NewValidationError("dashboard wallet address is required")
	}

	if err := validateDashboardWalletCreate(ctx, params, wallet); err != nil {
		return err
	}

	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO dashboard_wallet_users (wallet, user_id, is_delete, txhash, blocknumber, created_at, updated_at)
		VALUES ($1, $2, false, $3, $4, $5, $5)
		ON CONFLICT (wallet) DO UPDATE SET user_id = $2, is_delete = false, txhash = $3, blocknumber = $4, updated_at = $5
	`, wallet, params.UserID, params.TxHash, params.BlockNumber, params.BlockTime)
	return err
}

func validateDashboardWalletCreate(ctx context.Context, params *Params, wallet string) error {
	exists, err := userExists(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("user %d does not exist", params.UserID)
	}

	userWallet, err := getUserWallet(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}
	signerIsUser := strings.EqualFold(userWallet, params.Signer)
	signerIsWallet := strings.EqualFold(wallet, params.Signer)
	if !signerIsUser && !signerIsWallet {
		return NewValidationError("signer does not match user or dashboard wallet")
	}

	if err := verifyDashboardWalletSignatures(params, wallet, userWallet); err != nil {
		return err
	}

	// Check wallet not already assigned to an active user
	var existingIsDelete bool
	var existingFound bool
	err = params.DBTX.QueryRow(ctx,
		"SELECT is_delete FROM dashboard_wallet_users WHERE wallet = $1",
		wallet).Scan(&existingIsDelete)
	if err == nil {
		existingFound = true
	}
	if existingFound && !existingIsDelete {
		return NewValidationError("dashboard wallet %s already has an assigned user", wallet)
	}

	return nil
}

// verifyDashboardWalletSignatures verifies wallet_signature (signed by dashboard wallet)
// and/or user_signature (signed by user wallet) via ecrecover.
func verifyDashboardWalletSignatures(params *Params, wallet, userWallet string) error {
	walletSig := extractSignature(params, "wallet_signature")
	userSig := extractSignature(params, "user_signature")

	if walletSig == nil && userSig == nil {
		return NewValidationError("wallet signature or user signature is required")
	}

	if walletSig != nil {
		recovered, err := recoverETHAddress(walletSig.message, walletSig.signature)
		if err != nil {
			return NewValidationError("invalid wallet_signature: %v", err)
		}
		if !strings.EqualFold(recovered, wallet) {
			return NewValidationError("wallet_signature was not signed by the dashboard wallet")
		}
	}

	if userSig != nil {
		recovered, err := recoverETHAddress(userSig.message, userSig.signature)
		if err != nil {
			return NewValidationError("invalid user_signature: %v", err)
		}
		if !strings.EqualFold(recovered, userWallet) {
			return NewValidationError("user_signature was not signed by the user wallet")
		}
	}

	return nil
}

type sigData struct {
	message   string
	signature string
}

// extractSignature pulls message+signature from a metadata field like
// {"wallet_signature": {"message": "...", "signature": "..."}}.
func extractSignature(params *Params, key string) *sigData {
	raw, ok := params.MetadataJSON(key)
	if !ok {
		return nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	msg, _ := obj["message"].(string)
	sig, _ := obj["signature"].(string)
	if msg == "" || sig == "" {
		return nil
	}
	return &sigData{message: msg, signature: sig}
}

type dashboardWalletDeleteHandler struct{}

func (h *dashboardWalletDeleteHandler) EntityType() string { return EntityTypeDashboardWalletUser }
func (h *dashboardWalletDeleteHandler) Action() string     { return ActionDelete }

func (h *dashboardWalletDeleteHandler) Handle(ctx context.Context, params *Params) error {
	wallet := strings.ToLower(params.MetadataString("wallet"))
	if wallet == "" {
		return NewValidationError("dashboard wallet address is required")
	}

	if err := validateDashboardWalletDelete(ctx, params, wallet); err != nil {
		return err
	}

	_, err := params.DBTX.Exec(ctx, `
		UPDATE dashboard_wallet_users SET is_delete = true, txhash = $1, blocknumber = $2, updated_at = $3
		WHERE wallet = $4
	`, params.TxHash, params.BlockNumber, params.BlockTime, wallet)
	return err
}

func validateDashboardWalletDelete(ctx context.Context, params *Params, wallet string) error {
	exists, err := userExists(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("user %d does not exist", params.UserID)
	}

	var assignedUserID int64
	var isDelete bool
	err = params.DBTX.QueryRow(ctx,
		"SELECT user_id, is_delete FROM dashboard_wallet_users WHERE wallet = $1",
		wallet).Scan(&assignedUserID, &isDelete)
	if err != nil {
		return NewValidationError("dashboard wallet %s does not exist", wallet)
	}
	if isDelete {
		return NewValidationError("dashboard wallet %s is already deleted", wallet)
	}

	userWallet, err := getUserWallet(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}
	signerIsUser := strings.EqualFold(userWallet, params.Signer)
	signerIsWallet := strings.EqualFold(wallet, params.Signer)
	if !signerIsUser && !signerIsWallet {
		return NewValidationError("signer does not match user or dashboard wallet")
	}

	if signerIsUser && assignedUserID != params.UserID {
		return NewValidationError("user is not assigned to this wallet")
	}

	return nil
}

func DashboardWalletCreate() Handler { return &dashboardWalletCreateHandler{} }
func DashboardWalletDelete() Handler { return &dashboardWalletDeleteHandler{} }
