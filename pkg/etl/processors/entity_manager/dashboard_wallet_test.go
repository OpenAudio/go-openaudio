package entity_manager

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
)

// testETHSign creates a personal_sign signature using the given key and returns the hex-encoded sig.
func testETHSign(t *testing.T, key *ecdsa.PrivateKey, message string) string {
	t.Helper()
	hash := accounts.TextHash([]byte(message))
	sig, err := crypto.Sign(hash, key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Adjust v: go-ethereum produces 0/1, wallets produce 27/28
	sig[64] += 27
	return "0x" + hex.EncodeToString(sig)
}

func testETHAddress(key *ecdsa.PrivateKey) string {
	return crypto.PubkeyToAddress(key.PublicKey).Hex()
}

func TestDashboardWalletCreate_TxType(t *testing.T) {
	h := DashboardWalletCreate()
	if h.EntityType() != EntityTypeDashboardWalletUser {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeDashboardWalletUser)
	}
	if h.Action() != ActionCreate {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionCreate)
	}
}

func TestDashboardWalletCreate_Success(t *testing.T) {
	pool := setupTestDB(t)

	// Generate keys for user and dashboard wallet
	userKey, _ := crypto.GenerateKey()
	dashKey, _ := crypto.GenerateKey()
	userAddr := testETHAddress(userKey)
	dashAddr := testETHAddress(dashKey)

	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, userAddr, "user1")

	msg := "Connecting Audius dashboard wallet"
	sig := testETHSign(t, dashKey, msg)
	meta := fmt.Sprintf(`{"wallet":"%s","wallet_signature":{"message":"%s","signature":"%s"}}`, dashAddr, msg, sig)
	params := buildParams(t, pool, EntityTypeDashboardWalletUser, ActionCreate, uid, 0, userAddr, meta)
	mustHandle(t, DashboardWalletCreate(), params)

	var walletUserID int64
	err := pool.QueryRow(context.Background(),
		"SELECT user_id FROM dashboard_wallet_users WHERE wallet = $1", dashAddr).Scan(&walletUserID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if walletUserID != uid {
		t.Errorf("user_id = %d, want %d", walletUserID, uid)
	}
}

func TestDashboardWalletCreate_RejectsMissingWallet(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xuser1", "user1")
	params := buildParams(t, pool, EntityTypeDashboardWalletUser, ActionCreate, uid, 0, "0xUser1", `{}`)
	mustReject(t, DashboardWalletCreate(), params, "wallet address is required")
}

func TestDashboardWalletCreate_RejectsMissingSig(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xuser1", "user1")
	meta := `{"wallet":"0xdashboard1"}`
	params := buildParams(t, pool, EntityTypeDashboardWalletUser, ActionCreate, uid, 0, "0xUser1", meta)
	mustReject(t, DashboardWalletCreate(), params, "signature is required")
}

func TestDashboardWalletCreate_RejectsInvalidSig(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	userKey, _ := crypto.GenerateKey()
	dashKey, _ := crypto.GenerateKey()
	userAddr := testETHAddress(userKey)
	dashAddr := testETHAddress(dashKey)
	seedUser(t, pool, uid, userAddr, "user1")

	// Sign with user key but claim it's wallet_signature (should fail — not signed by dashboard wallet)
	msg := "Connecting Audius dashboard wallet"
	sig := testETHSign(t, userKey, msg)
	meta := fmt.Sprintf(`{"wallet":"%s","wallet_signature":{"message":"%s","signature":"%s"}}`, dashAddr, msg, sig)
	params := buildParams(t, pool, EntityTypeDashboardWalletUser, ActionCreate, uid, 0, userAddr, meta)
	mustReject(t, DashboardWalletCreate(), params, "wallet_signature was not signed by the dashboard wallet")
}

func TestDashboardWalletCreate_RejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	userKey, _ := crypto.GenerateKey()
	dashKey, _ := crypto.GenerateKey()
	userAddr := testETHAddress(userKey)
	dashAddr := testETHAddress(dashKey)

	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, userAddr, "user1")

	msg := "Connecting Audius dashboard wallet"
	sig := testETHSign(t, dashKey, msg)
	meta := fmt.Sprintf(`{"wallet":"%s","wallet_signature":{"message":"%s","signature":"%s"}}`, dashAddr, msg, sig)
	mustHandle(t, DashboardWalletCreate(), buildParams(t, pool, EntityTypeDashboardWalletUser, ActionCreate, uid, 0, userAddr, meta))
	mustReject(t, DashboardWalletCreate(), buildParams(t, pool, EntityTypeDashboardWalletUser, ActionCreate, uid, 0, userAddr, meta), "already has an assigned user")
}

func TestDashboardWalletDelete_Success(t *testing.T) {
	pool := setupTestDB(t)
	userKey, _ := crypto.GenerateKey()
	dashKey, _ := crypto.GenerateKey()
	userAddr := testETHAddress(userKey)
	dashAddr := testETHAddress(dashKey)

	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, userAddr, "user1")

	msg := "Connecting Audius dashboard wallet"
	sig := testETHSign(t, dashKey, msg)
	createMeta := fmt.Sprintf(`{"wallet":"%s","wallet_signature":{"message":"%s","signature":"%s"}}`, dashAddr, msg, sig)
	mustHandle(t, DashboardWalletCreate(), buildParams(t, pool, EntityTypeDashboardWalletUser, ActionCreate, uid, 0, userAddr, createMeta))

	deleteMeta := fmt.Sprintf(`{"wallet":"%s"}`, dashAddr)
	mustHandle(t, DashboardWalletDelete(), buildParams(t, pool, EntityTypeDashboardWalletUser, ActionDelete, uid, 0, userAddr, deleteMeta))

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM dashboard_wallet_users WHERE wallet = $1", dashAddr).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isDelete {
		t.Error("expected is_delete = true")
	}
}

func TestDashboardWalletDelete_RejectsNonexistent(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xuser1", "user1")
	params := buildParams(t, pool, EntityTypeDashboardWalletUser, ActionDelete, uid, 0, "0xUser1", `{"wallet":"0xnotfound"}`)
	mustReject(t, DashboardWalletDelete(), params, "does not exist")
}
