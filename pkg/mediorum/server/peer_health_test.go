package server

import (
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/registrar"
	"golang.org/x/exp/slices"
)

// Past the replica set the rendezvous ranking says nothing about who holds a
// blob, so repair falls back to store-all peers. Selection must exclude self,
// exclude unhealthy peers, stay capped, and spread across CIDs so a handful of
// store-all nodes don't all get hammered in the same order.
func TestFindStoreAllPeers(t *testing.T) {
	now := time.Now()
	ss := &MediorumServer{
		Config: MediorumConfig{Self: registrar.Peer{Host: "https://self"}},
		peerHealths: map[string]*PeerHealth{
			"https://self":    {StoreAll: true, LastHealthy: now},
			"https://sa-a":    {StoreAll: true, LastHealthy: now},
			"https://sa-b":    {StoreAll: true, LastHealthy: now},
			"https://sa-c":    {StoreAll: true, LastHealthy: now},
			"https://sa-dead": {StoreAll: true, LastHealthy: now.Add(-24 * time.Hour)},
			"https://plain":   {StoreAll: false, LastHealthy: now},
		},
	}

	got := ss.findStoreAllPeers("somecid", time.Hour, 2)
	if len(got) != 2 {
		t.Fatalf("limit not respected: %v", got)
	}
	for _, h := range got {
		switch h {
		case "https://self":
			t.Fatalf("must not select self: %v", got)
		case "https://plain":
			t.Fatalf("must not select non-store-all peer: %v", got)
		case "https://sa-dead":
			t.Fatalf("must not select stale peer: %v", got)
		}
	}

	// Deterministic for a given key...
	if again := ss.findStoreAllPeers("somecid", time.Hour, 2); !slices.Equal(got, again) {
		t.Fatalf("not deterministic: %v vs %v", got, again)
	}

	// ...but not every CID starts at the same host, or one node absorbs it all.
	seenFirst := map[string]bool{}
	for _, cid := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		if p := ss.findStoreAllPeers(cid, time.Hour, 1); len(p) == 1 {
			seenFirst[p[0]] = true
		}
	}
	if len(seenFirst) < 2 {
		t.Fatalf("fallback always picks the same host, load will concentrate: %v", seenFirst)
	}
}

func TestFindStoreAllPeersEmptyCases(t *testing.T) {
	ss := &MediorumServer{
		Config:      MediorumConfig{Self: registrar.Peer{Host: "https://self"}},
		peerHealths: map[string]*PeerHealth{"https://plain": {LastHealthy: time.Now()}},
	}
	if got := ss.findStoreAllPeers("cid", time.Hour, 2); len(got) != 0 {
		t.Fatalf("no store-all peers: got %v", got)
	}
	if got := ss.findStoreAllPeers("cid", time.Hour, 0); got != nil {
		t.Fatalf("limit 0 must select nothing: got %v", got)
	}
}
