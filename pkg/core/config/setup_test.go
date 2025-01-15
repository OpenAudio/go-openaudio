package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
	selectedPersistentPeers2 := strings.Split(nodes, ",")
	if len(selectedPersistentPeers2) != 3 {
		t.Fatalf("expected 3 persistent peers, got %d", len(selectedPersistentPeers))
	}

	require.NotEqual(t, nodes, nodes2)
}
