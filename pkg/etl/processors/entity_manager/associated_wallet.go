package entity_manager

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
)

type associatedWalletCreateHandler struct{}

func (h *associatedWalletCreateHandler) EntityType() string { return EntityTypeAssociatedWallet }
func (h *associatedWalletCreateHandler) Action() string     { return ActionCreate }

func (h *associatedWalletCreateHandler) Handle(ctx context.Context, params *Params) error {
	chain := params.MetadataString("chain")
	wallet := canonicalizeWallet(params.MetadataString("wallet"), chain)

	if err := validateAssociatedWalletCreate(ctx, params, wallet, chain); err != nil {
		return err
	}

	return insertAssociatedWallet(ctx, params, wallet, chain)
}

// insertAssociatedWallet claims the wallet for params.UserID and writes the new
// row. Shared by the production and genesis migration handlers.
func insertAssociatedWallet(ctx context.Context, params *Params, wallet, chain string) error {
	// Remove wallet from other users on the same chain (exclusive ownership)
	_, err := params.DBTX.Exec(ctx,
		"UPDATE associated_wallets SET is_current = false, is_delete = true WHERE wallet = $1 AND chain = $2 AND user_id != $3 AND is_current = true",
		wallet, chain, params.UserID)
	if err != nil {
		return err
	}

	_, err = params.DBTX.Exec(ctx,
		"UPDATE associated_wallets SET is_current = false WHERE user_id = $1 AND wallet = $2 AND chain = $3 AND is_current = true",
		params.UserID, wallet, chain)
	if err != nil {
		return err
	}

	// blockhash is NOT NULL on prod's associated_wallets table.
	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO associated_wallets (user_id, wallet, chain, is_current, is_delete, blockhash, blocknumber, created_at, updated_at)
		VALUES ($1, $2, $3, true, false, $4, $5, $6, $6)
	`, params.UserID, wallet, chain, params.BlockHash, params.BlockNumber, params.BlockTime)
	return err
}

func validateAssociatedWalletCreate(ctx context.Context, params *Params, wallet, chain string) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	if err := validateAssociatedWalletShape(wallet, chain); err != nil {
		return err
	}

	return verifyAssociatedWalletSignature(params, wallet, chain)
}

// validateAssociatedWalletShape checks the fields that must be well-formed
// regardless of how the row arrived. Shared by the production and genesis
// migration handlers.
func validateAssociatedWalletShape(wallet, chain string) error {
	if wallet == "" {
		return NewValidationError("wallet address is required")
	}
	if chain == "" {
		return NewValidationError("chain is required")
	}
	if chain != "eth" && chain != "sol" {
		return NewValidationError("chain must be eth or sol, got %s", chain)
	}
	return nil
}

// verifyAssociatedWalletSignature verifies the wallet_signature proves ownership of the wallet.
// ETH wallets use personal_sign (ecrecover), SOL wallets use ed25519.
func verifyAssociatedWalletSignature(params *Params, wallet, chain string) error {
	sig := extractSignature(params, "wallet_signature")
	if sig == nil {
		return NewValidationError("wallet_signature is required")
	}

	switch chain {
	case "eth":
		recovered, err := recoverETHAddress(sig.message, sig.signature)
		if err != nil {
			return NewValidationError("invalid wallet_signature: %v", err)
		}
		if !strings.EqualFold(recovered, wallet) {
			return NewValidationError("wallet_signature was not signed by wallet %s", wallet)
		}
	case "sol":
		if err := verifySolSignature(sig.message, sig.signature, wallet); err != nil {
			return NewValidationError("invalid sol wallet_signature: %v", err)
		}
	}
	return nil
}

// canonicalizeWallet normalizes a wallet address for storage and lookup.
// ETH (hex) addresses are case-insensitive; we canonicalize to lowercase so
// that the same wallet typed in any case maps to a single row. Solana
// addresses are base58 — case-sensitive by construction — and MUST be
// preserved verbatim (`L`→`l` would produce a base58-invalid character).
// When the chain isn't known (e.g., delete tx that omits "chain"), the
// `0x` prefix is a reliable ETH discriminator.
func canonicalizeWallet(wallet, chain string) string {
	if chain == "eth" || strings.HasPrefix(wallet, "0x") || strings.HasPrefix(wallet, "0X") {
		return strings.ToLower(wallet)
	}
	return wallet
}

// verifySolSignature verifies an ed25519 signature from a Solana wallet.
// The signature and public key are expected as base58-encoded strings.
func verifySolSignature(message, signatureB64, walletB58 string) error {
	pubKeyBytes, err := base58Decode(walletB58)
	if err != nil {
		return fmt.Errorf("invalid solana wallet address: %w", err)
	}
	if len(pubKeyBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid solana public key length: %d", len(pubKeyBytes))
	}

	sigBytes, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		// Try base58 as fallback
		sigBytes, err = base58Decode(signatureB64)
		if err != nil {
			return fmt.Errorf("invalid signature encoding: %w", err)
		}
	}
	if len(sigBytes) != ed25519.SignatureSize {
		return fmt.Errorf("invalid signature length: %d", len(sigBytes))
	}

	if !ed25519.Verify(pubKeyBytes, []byte(message), sigBytes) {
		return fmt.Errorf("signature verification failed")
	}
	return nil
}

// base58Decode decodes a base58 (Bitcoin alphabet) string.
func base58Decode(s string) ([]byte, error) {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	result := []byte{0}
	for _, c := range []byte(s) {
		carry := strings.IndexByte(alphabet, c)
		if carry < 0 {
			return nil, fmt.Errorf("invalid base58 character: %c", c)
		}
		for i := range result {
			carry += int(result[i]) * 58
			result[i] = byte(carry & 0xff)
			carry >>= 8
		}
		for carry > 0 {
			result = append(result, byte(carry&0xff))
			carry >>= 8
		}
	}
	// Count leading '1's
	for _, c := range []byte(s) {
		if c != '1' {
			break
		}
		result = append(result, 0)
	}
	// Reverse
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result, nil
}

type associatedWalletDeleteHandler struct{}

func (h *associatedWalletDeleteHandler) EntityType() string { return EntityTypeAssociatedWallet }
func (h *associatedWalletDeleteHandler) Action() string     { return ActionDelete }

func (h *associatedWalletDeleteHandler) Handle(ctx context.Context, params *Params) error {
	// Delete tx may not include `chain` in metadata (chain is implicit in the
	// stored row), so canonicalize off the wallet format: `0x`-prefixed → ETH
	// hex → lowercase; otherwise (Solana base58) preserve case.
	chain := params.MetadataString("chain")
	wallet := canonicalizeWallet(params.MetadataString("wallet"), chain)

	if err := validateAssociatedWalletDelete(ctx, params, wallet, chain); err != nil {
		return err
	}

	// Match validateAssociatedWalletDelete's lookup (no chain filter) — the
	// (user_id, wallet) pair already uniquely identifies the row, and the
	// delete metadata isn't required to repeat `chain`.
	_, err := params.DBTX.Exec(ctx,
		"UPDATE associated_wallets SET is_current = false, is_delete = true, updated_at = $1 WHERE user_id = $2 AND wallet = $3 AND is_current = true",
		params.BlockTime, params.UserID, wallet)
	return err
}

func validateAssociatedWalletDelete(ctx context.Context, params *Params, wallet, chain string) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	if wallet == "" {
		return NewValidationError("wallet address is required")
	}

	var exists bool
	err := params.DBTX.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM associated_wallets WHERE user_id = $1 AND wallet = $2 AND is_current = true AND is_delete = false)",
		params.UserID, wallet).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("associated wallet %s not found for user %d", wallet, params.UserID)
	}

	return nil
}

func AssociatedWalletCreate() Handler { return &associatedWalletCreateHandler{} }
func AssociatedWalletDelete() Handler { return &associatedWalletDeleteHandler{} }
