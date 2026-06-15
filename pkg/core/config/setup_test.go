package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// test that moduloPersistentPeers returns the expected number of persistent peers
// and that it changes the 3 based on the provided eth address
func TestModuloPersistentPeers(t *testing.T) {
	nodes := moduloPersistentPeers("0xff432F81D0eb77DA5973Cf55e24A897882fdd3E6", ProdPersistentPeers, 3)
	selectedPersistentPeers := strings.Split(nodes, ",")
	if len(selectedPersistentPeers) != 3 {
		t.Fatalf("expected 3 persistent peers, got %d", len(selectedPersistentPeers))
	}

	nodes2 := moduloPersistentPeers("0xE019F1Ad9803cfC83e11D37Da442c9Dc8D8d82a6", ProdPersistentPeers, 3)
	selectedPersistentPeers2 := strings.Split(nodes2, ",")
	if len(selectedPersistentPeers2) != 3 {
		t.Fatalf("expected 3 persistent peers, got %d", len(selectedPersistentPeers))
	}

	require.NotEqual(t, nodes, nodes2)
}

func TestProdPersistentPeersBroadAndParseable(t *testing.T) {
	peers := strings.Split(ProdPersistentPeers, ",")
	require.GreaterOrEqual(t, len(peers), 20, "prod should not bootstrap from a tiny hub-only peer set")

	seen := map[string]bool{}
	for _, peer := range peers {
		require.NotEmpty(t, peer)
		addr, err := p2p.NewNetAddressString(peer)
		require.NoError(t, err, "peer %q should be a valid CometBFT net address", peer)
		id := string(addr.ID)
		require.False(t, seen[id], "duplicate peer id %s", addr.ID)
		seen[id] = true
	}

	for _, staleIP := range []string{
		"34.173.190.5:26656",
		"34.121.217.14:26656",
		"34.67.133.214:26656",
		"34.46.116.59:26656",
		"35.222.113.66:26656",
		"34.28.164.31:26656",
	} {
		require.NotContains(t, ProdPersistentPeers, staleIP)
	}
}

func TestEnsurePrivValidator(t *testing.T) {
	derivedKey := ed25519.GenPrivKey()
	staleKey := ed25519.GenPrivKey()
	logger := zaptest.NewLogger(t)

	paths := func(t *testing.T) (string, string) {
		dir := t.TempDir()
		return filepath.Join(dir, "priv_validator_key.json"),
			filepath.Join(dir, "priv_validator_state.json")
	}

	t.Run("generates when files are missing", func(t *testing.T) {
		keyFile, stateFile := paths(t)

		pv, err := ensurePrivValidator(logger, &derivedKey, keyFile, stateFile)
		require.NoError(t, err)
		require.Equal(t, derivedKey.PubKey().Bytes(), pv.Key.PubKey.Bytes())
		require.FileExists(t, keyFile)
		require.FileExists(t, stateFile)
	})

	t.Run("loads existing matching files unchanged", func(t *testing.T) {
		keyFile, stateFile := paths(t)
		seed := privval.NewFilePV(&derivedKey, keyFile, stateFile)
		seed.Save()

		pv, err := ensurePrivValidator(logger, &derivedKey, keyFile, stateFile)
		require.NoError(t, err)
		require.Equal(t, derivedKey.PubKey().Bytes(), pv.Key.PubKey.Bytes())
		require.Equal(t, seed.GetAddress(), pv.GetAddress())
	})

	t.Run("regenerates mismatched files when never signed", func(t *testing.T) {
		keyFile, stateFile := paths(t)
		stale := privval.NewFilePV(&staleKey, keyFile, stateFile)
		stale.Save()
		require.NotEqual(t, derivedKey.PubKey().Bytes(), stale.Key.PubKey.Bytes())

		pv, err := ensurePrivValidator(logger, &derivedKey, keyFile, stateFile)
		require.NoError(t, err)
		require.Equal(t, derivedKey.PubKey().Bytes(), pv.Key.PubKey.Bytes(),
			"on-disk key should be regenerated to match the derived key")

		// And on-disk persisted state should reflect the new key on next load.
		reloaded := privval.LoadFilePV(keyFile, stateFile)
		require.Equal(t, derivedKey.PubKey().Bytes(), reloaded.Key.PubKey.Bytes())
	})

	t.Run("refuses to regenerate mismatched files with signing history", func(t *testing.T) {
		keyFile, stateFile := paths(t)
		stale := privval.NewFilePV(&staleKey, keyFile, stateFile)
		stale.LastSignState.Height = 42 // pretend this key has signed
		stale.Save()

		pv, err := ensurePrivValidator(logger, &derivedKey, keyFile, stateFile)
		require.Error(t, err)
		require.Nil(t, pv)
		require.Contains(t, err.Error(), "double-sign")
		require.Contains(t, err.Error(), "height 42")

		// Files must remain untouched so the operator can investigate.
		reloaded := privval.LoadFilePV(keyFile, stateFile)
		require.Equal(t, staleKey.PubKey().Bytes(), reloaded.Key.PubKey.Bytes())
		require.Equal(t, int64(42), reloaded.LastSignState.Height)
	})
}
