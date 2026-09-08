package server

import (
	"encoding/json"
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

// The parser reads a peer's health-check JSON, which is PeerHealth marshalled
// by that peer. Round-tripping through encoding/json ties the key the parser
// looks up to the struct tag that produces it: the two silently diverged once,
// leaving ReachablePeers empty on every node on the network.
func TestParseReachablePeersMatchesPeerHealthJSON(t *testing.T) {
	reachedAt := time.Now().UTC().Add(-30 * time.Second).Round(time.Nanosecond)

	blob, err := json.Marshal(map[string]*PeerHealth{
		"https://reached": {LastReachable: reachedAt},
		"https://never":   {},
	})
	if err != nil {
		t.Fatal(err)
	}
	var peerHealthsMap map[string]interface{}
	if err := json.Unmarshal(blob, &peerHealthsMap); err != nil {
		t.Fatal(err)
	}

	got := parseReachablePeers(peerHealthsMap)
	if len(got) != 2 {
		t.Fatalf("parser and PeerHealth's json tag have diverged: got %v", got)
	}
	if !got["https://reached"].Equal(reachedAt) {
		t.Errorf("lastReachable not recovered: got %v want %v", got["https://reached"], reachedAt)
	}
	if !got["https://never"].IsZero() {
		t.Errorf("a never-reached peer should decode to the zero time: got %v", got["https://never"])
	}
}

func TestParseReachablePeersSkipsMalformed(t *testing.T) {
	got := parseReachablePeers(map[string]interface{}{
		"https://not-an-object": "nope",
		"https://wrong-type":    map[string]interface{}{"lastReachable": 12345},
		"https://unparseable":   map[string]interface{}{"lastReachable": "not a timestamp"},
		"https://absent":        map[string]interface{}{"lastHealthy": "2026-09-08T05:39:57Z"},
		"https://good":          map[string]interface{}{"lastReachable": "2026-09-08T05:39:57.93811266Z"},
	})
	if len(got) != 1 {
		t.Fatalf("only the well-formed entry should survive: got %v", got)
	}
	if _, ok := got["https://good"]; !ok {
		t.Fatalf("well-formed entry dropped: got %v", got)
	}
}

// Once ReachablePeers is populated, numReachableBy picks up hosts sourced from
// a peer's own report -- including our own host, and any peer we have never
// successfully polled -- for which ss.peerHealths holds no entry. Dereferencing
// that missing entry panics, and the caller is a poller goroutine with no
// recover, so it would take the process down.
func TestGetReachableByMajorityButNotByHostSkipsUnknownHosts(t *testing.T) {
	now := time.Now()
	peers := []registrar.Peer{}
	for _, h := range []string{"https://self", "https://suspect", "https://a", "https://b", "https://c", "https://d", "https://e"} {
		peers = append(peers, registrar.Peer{Host: h})
	}

	// Every voter reports reaching a host we have no entry for, plus us.
	voterView := func() map[string]time.Time {
		return map[string]time.Time{
			"https://unpolled": now,
			"https://self":     now,
		}
	}
	ss := &MediorumServer{
		Config: MediorumConfig{Self: registrar.Peer{Host: "https://self"}, Peers: peers},
		peerHealths: map[string]*PeerHealth{
			"https://suspect": {LastReachable: now, ReachablePeers: map[string]time.Time{}},
			"https://a":       {LastReachable: now, ReachablePeers: voterView()},
			"https://b":       {LastReachable: now, ReachablePeers: voterView()},
			"https://c":       {LastReachable: now, ReachablePeers: voterView()},
			"https://d":       {LastReachable: now, ReachablePeers: voterView()},
			"https://e":       {LastReachable: now, ReachablePeers: voterView()},
		},
	}

	got := ss.getReachableByMajorityButNotByHost("https://suspect")
	if slices.Contains(got, "https://unpolled") || slices.Contains(got, "https://self") {
		t.Fatalf("hosts we hold no health for are a gap in our data, not evidence: %v", got)
	}
}

// The flip side: a peer the majority reaches, that the suspect host does not,
// is what this check exists to surface.
func TestGetReachableByMajorityButNotByHostFlagsUnreachable(t *testing.T) {
	now := time.Now()
	hosts := []string{"https://self", "https://suspect", "https://a", "https://b", "https://c", "https://d", "https://e"}
	peers := []registrar.Peer{}
	for _, h := range hosts {
		peers = append(peers, registrar.Peer{Host: h})
	}

	peerHealths := map[string]*PeerHealth{
		// The suspect reports reaching everyone except "e".
		"https://suspect": {LastReachable: now, ReachablePeers: map[string]time.Time{
			"https://a": now, "https://b": now, "https://c": now, "https://d": now,
		}},
	}
	for _, h := range []string{"https://a", "https://b", "https://c", "https://d", "https://e"} {
		// Every other peer reaches "e", and reports the suspect reaching it too.
		peerHealths[h] = &PeerHealth{LastReachable: now, ReachablePeers: map[string]time.Time{
			"https://e": now, "https://suspect": now,
		}}
	}
	// "e" is the odd one out: it does not report reaching the suspect.
	peerHealths["https://e"].ReachablePeers = map[string]time.Time{"https://a": now}

	ss := &MediorumServer{
		Config:      MediorumConfig{Self: registrar.Peer{Host: "https://self"}, Peers: peers},
		peerHealths: peerHealths,
	}

	if got := ss.getReachableByMajorityButNotByHost("https://suspect"); !slices.Contains(got, "https://e") {
		t.Fatalf("peer reachable by the majority but not by the suspect should be flagged: %v", got)
	}
}

// End-to-end over the test network: this exercises the whole path the bug hid
// in -- a peer serves its real /health-check, and the poller has to recover the
// reachable set from that JSON. A unit test on parseReachablePeers alone would
// not have caught the original divergence in serialization.
func TestHealthPollerPopulatesReachablePeers(t *testing.T) {
	ss := testNetwork[0]

	deadline := time.Now().Add(10 * time.Second)
	for {
		ss.peerHealthsMutex.RLock()
		total := 0
		for _, ph := range ss.peerHealths {
			total += len(ph.ReachablePeers)
		}
		ss.peerHealthsMutex.RUnlock()
		if total > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no peer reported a reachable set: the poller and PeerHealth's json tags have diverged")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
