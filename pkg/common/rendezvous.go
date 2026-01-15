package common

import (
	"bytes"
	"crypto/sha256"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"

	"golang.org/x/exp/slices"
)

type NodeTuple struct {
	addr  string
	score []byte
}

type NodeTuples []NodeTuple

func (s NodeTuples) Len() int      { return len(s) }
func (s NodeTuples) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s NodeTuples) Less(i, j int) bool {
	c := bytes.Compare(s[i].score, s[j].score)
	if c == 0 {
		return s[i].addr < s[j].addr
	}
	return c == -1
}

// Returns the first `size` number of addresses from a list of all validators sorted
// by a hashing function. The hashing is seeded according to the given key.
func GetAttestorRendezvous(validatorAddresses []string, key []byte, size int) map[string]bool {
	tuples := make(NodeTuples, len(validatorAddresses))

	hasher := sha256.New()
	for i, addr := range validatorAddresses {
		hasher.Reset()
		io.WriteString(hasher, addr)
		hasher.Write(key)
		tuples[i] = NodeTuple{addr, hasher.Sum(nil)}
	}
	sort.Sort(tuples)
	result := make(map[string]bool, len(validatorAddresses))
	bound := min(len(tuples), size)
	for i, tup := range tuples {
		if i >= bound {
			break
		}
		result[tup.addr] = true
	}
	return result
}

type HostTuple struct {
	host  string
	score []byte
}

type HostTuples []HostTuple

func (s HostTuples) Len() int      { return len(s) }
func (s HostTuples) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s HostTuples) Less(i, j int) bool {
	c := bytes.Compare(s[i].score, s[j].score)
	if c == 0 {
		return s[i].host < s[j].host
	}
	return c == -1
}

// RendezvousHasher provides thread-safe rendezvous hashing for host selection.
type RendezvousHasher struct {
	mu    sync.RWMutex
	hosts []string
}

// NewRendezvousHasher creates a new RendezvousHasher with the given hosts.
// Dead hosts, invalid URLs, and duplicates are filtered out.
func NewRendezvousHasher(hosts []string, deadHosts []string) *RendezvousHasher {
	liveHosts := filterLiveHosts(hosts, deadHosts)
	return &RendezvousHasher{
		hosts: liveHosts,
	}
}

// filterLiveHosts filters out dead hosts, invalid URLs, and duplicates.
func filterLiveHosts(hosts []string, deadHosts []string) []string {
	liveHosts := make([]string, 0, len(hosts))
	for _, h := range hosts {
		// Check if host is in dead hosts list
		isDead := false
		for _, dead := range deadHosts {
			if strings.Contains(h, dead) || h == dead {
				isDead = true
				break
			}
		}
		if isDead {
			continue
		}

		// Check if URL is valid
		if _, err := url.Parse(h); err != nil {
			continue
		}

		// Check for duplicates
		if slices.Contains(liveHosts, h) {
			continue
		}

		liveHosts = append(liveHosts, h)
	}
	return liveHosts
}

// UpdateHosts updates the list of hosts in a thread-safe manner.
// Dead hosts, invalid URLs, and duplicates are filtered out.
func (rh *RendezvousHasher) UpdateHosts(hosts []string, deadHosts []string) {
	rh.mu.Lock()
	defer rh.mu.Unlock()
	rh.hosts = filterLiveHosts(hosts, deadHosts)
}

// Rank returns a ranked list of hosts for the given key.
// The ranking is deterministic based on rendezvous hashing.
func (rh *RendezvousHasher) Rank(key string) []string {
	rh.mu.RLock()
	defer rh.mu.RUnlock()

	tuples := make(HostTuples, len(rh.hosts))
	keyBytes := []byte(key)
	hasher := sha256.New()
	for idx, host := range rh.hosts {
		hasher.Reset()
		io.WriteString(hasher, host)
		hasher.Write(keyBytes)
		tuples[idx] = HostTuple{host, hasher.Sum(nil)}
	}
	sort.Sort(tuples)
	result := make([]string, len(rh.hosts))
	for idx, tup := range tuples {
		result[idx] = tup.host
	}
	return result
}
