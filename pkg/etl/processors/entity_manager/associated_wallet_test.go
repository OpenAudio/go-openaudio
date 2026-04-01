package entity_manager

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestAssociatedWalletCreate_TxType(t *testing.T) {
	h := AssociatedWalletCreate()
	if h.EntityType() != EntityTypeAssociatedWallet {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeAssociatedWallet)
	}
	if h.Action() != ActionCreate {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionCreate)
	}
}

func TestAssociatedWalletCreate_ETH_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	userKey, _ := crypto.GenerateKey()
	userAddr := testETHAddress(userKey)
	seedUser(t, pool, uid, userAddr, "user1")

	walletKey, _ := crypto.GenerateKey()
	walletAddr := testETHAddress(walletKey)
	msg := "Connecting Audius wallet"
	sig := testETHSign(t, walletKey, msg)

	meta := fmt.Sprintf(`{"wallet":"%s","chain":"eth","wallet_signature":{"message":"%s","signature":"%s"}}`, walletAddr, msg, sig)
	params := buildParams(t, pool, EntityTypeAssociatedWallet, ActionCreate, uid, 0, userAddr, meta)
	mustHandle(t, AssociatedWalletCreate(), params)

	var wallet, chain string
	err := pool.QueryRow(context.Background(),
		"SELECT wallet, chain FROM associated_wallets WHERE user_id = $1 AND is_current = true AND is_delete = false",
		uid).Scan(&wallet, &chain)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if chain != "eth" {
		t.Errorf("chain = %q, want eth", chain)
	}
}

func TestAssociatedWalletCreate_ETH_RejectsWrongSigner(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	userKey, _ := crypto.GenerateKey()
	userAddr := testETHAddress(userKey)
	seedUser(t, pool, uid, userAddr, "user1")

	walletKey, _ := crypto.GenerateKey()
	walletAddr := testETHAddress(walletKey)
	// Sign with user key instead of wallet key
	msg := "Connecting Audius wallet"
	sig := testETHSign(t, userKey, msg)

	meta := fmt.Sprintf(`{"wallet":"%s","chain":"eth","wallet_signature":{"message":"%s","signature":"%s"}}`, walletAddr, msg, sig)
	params := buildParams(t, pool, EntityTypeAssociatedWallet, ActionCreate, uid, 0, userAddr, meta)
	mustReject(t, AssociatedWalletCreate(), params, "wallet_signature was not signed by wallet")
}

func TestAssociatedWalletCreate_SOL_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	userKey, _ := crypto.GenerateKey()
	userAddr := testETHAddress(userKey)
	seedUser(t, pool, uid, userAddr, "user1")

	// Generate ed25519 key pair for Solana wallet
	pub, priv, _ := ed25519.GenerateKey(nil)
	solAddr := base58Encode(pub)

	msg := "Connecting Audius wallet"
	sigBytes := ed25519.Sign(priv, []byte(msg))
	sigB64 := base64.StdEncoding.EncodeToString(sigBytes)

	meta := fmt.Sprintf(`{"wallet":"%s","chain":"sol","wallet_signature":{"message":"%s","signature":"%s"}}`, solAddr, msg, sigB64)
	params := buildParams(t, pool, EntityTypeAssociatedWallet, ActionCreate, uid, 0, userAddr, meta)
	mustHandle(t, AssociatedWalletCreate(), params)

	var wallet, chain string
	err := pool.QueryRow(context.Background(),
		"SELECT wallet, chain FROM associated_wallets WHERE user_id = $1 AND is_current = true AND is_delete = false",
		uid).Scan(&wallet, &chain)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if chain != "sol" {
		t.Errorf("chain = %q, want sol", chain)
	}
}

func TestAssociatedWalletCreate_RejectsMissingWallet(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xuser1", "user1")
	meta := `{"chain":"eth"}`
	params := buildParams(t, pool, EntityTypeAssociatedWallet, ActionCreate, uid, 0, "0xUser1", meta)
	mustReject(t, AssociatedWalletCreate(), params, "wallet address is required")
}

func TestAssociatedWalletCreate_RejectsMissingChain(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xuser1", "user1")
	meta := `{"wallet":"0xassoc1"}`
	params := buildParams(t, pool, EntityTypeAssociatedWallet, ActionCreate, uid, 0, "0xUser1", meta)
	mustReject(t, AssociatedWalletCreate(), params, "chain is required")
}

func TestAssociatedWalletCreate_RejectsInvalidChain(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xuser1", "user1")
	meta := `{"wallet":"0xassoc1","chain":"btc"}`
	params := buildParams(t, pool, EntityTypeAssociatedWallet, ActionCreate, uid, 0, "0xUser1", meta)
	mustReject(t, AssociatedWalletCreate(), params, "chain must be eth or sol")
}

func TestAssociatedWalletCreate_RejectsMissingSig(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xuser1", "user1")
	meta := `{"wallet":"0xassoc1","chain":"eth"}`
	params := buildParams(t, pool, EntityTypeAssociatedWallet, ActionCreate, uid, 0, "0xUser1", meta)
	mustReject(t, AssociatedWalletCreate(), params, "wallet_signature is required")
}

func TestAssociatedWalletDelete_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	userKey, _ := crypto.GenerateKey()
	walletKey, _ := crypto.GenerateKey()
	userAddr := testETHAddress(userKey)
	walletAddr := testETHAddress(walletKey)
	seedUser(t, pool, uid, userAddr, "user1")

	msg := "Connecting Audius wallet"
	sig := testETHSign(t, walletKey, msg)
	createMeta := fmt.Sprintf(`{"wallet":"%s","chain":"eth","wallet_signature":{"message":"%s","signature":"%s"}}`, walletAddr, msg, sig)
	mustHandle(t, AssociatedWalletCreate(), buildParams(t, pool, EntityTypeAssociatedWallet, ActionCreate, uid, 0, userAddr, createMeta))

	deleteMeta := fmt.Sprintf(`{"wallet":"%s"}`, walletAddr)
	mustHandle(t, AssociatedWalletDelete(), buildParams(t, pool, EntityTypeAssociatedWallet, ActionDelete, uid, 0, userAddr, deleteMeta))

	var count int
	err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM associated_wallets WHERE user_id = $1 AND is_current = true AND is_delete = false", uid).Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 active wallets, got %d", count)
	}
}

func TestAssociatedWalletDelete_RejectsNonexistent(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xuser1", "user1")
	params := buildParams(t, pool, EntityTypeAssociatedWallet, ActionDelete, uid, 0, "0xUser1", `{"wallet":"0xnotfound"}`)
	mustReject(t, AssociatedWalletDelete(), params, "not found")
}

// base58Encode encodes bytes to base58 (Bitcoin alphabet).
func base58Encode(b []byte) string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	// Count leading zeros
	var zeros int
	for _, v := range b {
		if v != 0 {
			break
		}
		zeros++
	}
	// Convert to base58
	size := len(b)*138/100 + 1
	buf := make([]byte, size)
	var length int
	for _, v := range b {
		carry := int(v)
		for i := 0; i < length || carry != 0; i++ {
			if i < length {
				carry += 256 * int(buf[i])
			}
			buf[i] = byte(carry % 58)
			carry /= 58
			if i >= length {
				length = i + 1
			}
		}
	}
	result := make([]byte, zeros+length)
	for i := 0; i < zeros; i++ {
		result[i] = '1'
	}
	for i := 0; i < length; i++ {
		result[zeros+i] = alphabet[buf[length-1-i]]
	}
	return string(result)
}
