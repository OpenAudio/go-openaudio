package fuzz

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// NodeID is the stable name used by scenarios to refer to a node.
type NodeID string

// NodeSpec describes one node from the harness point of view.
//
// Endpoint is the externally reachable HTTP(S) endpoint used for observations.
// Command is optional; when it is set, ProcessNetwork can start and stop the
// node as a local process.
type NodeSpec struct {
	ID              NodeID
	Endpoint        string
	Command         []string
	Env             map[string]string
	Dir             string
	LogPath         string
	StartupTimeout  time.Duration
	ShutdownTimeout time.Duration
}

// NetworkSpec describes a network under test.
type NetworkSpec struct {
	Name  string
	Nodes []NodeSpec
}

func (s NetworkSpec) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("network name is required")
	}
	if len(s.Nodes) == 0 {
		return errors.New("at least one node is required")
	}

	seen := make(map[NodeID]struct{}, len(s.Nodes))
	for i, node := range s.Nodes {
		if node.ID == "" {
			return fmt.Errorf("node %d has empty id", i)
		}
		if _, ok := seen[node.ID]; ok {
			return fmt.Errorf("duplicate node id %q", node.ID)
		}
		seen[node.ID] = struct{}{}
	}
	return nil
}

func (s NetworkSpec) Node(id NodeID) (NodeSpec, bool) {
	for _, node := range s.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return NodeSpec{}, false
}

func (s NetworkSpec) NodeIDs() []NodeID {
	ids := make([]NodeID, 0, len(s.Nodes))
	for _, node := range s.Nodes {
		ids = append(ids, node.ID)
	}
	sort.Slice(ids, func(i, j int) bool {
		return ids[i] < ids[j]
	})
	return ids
}

// NodeStatus is a single observation of one node.
type NodeStatus struct {
	ID               NodeID
	Endpoint         string
	Reachable        bool
	Ready            bool
	Live             bool
	Synced           bool
	Height           int64
	BlockHash        string
	Version          string
	Git              string
	ValidatorPower   int64
	ProcessState     string
	ProcessError     string
	ObservationError string
	ObservedAt       time.Time
}

// Snapshot is a point-in-time observation of the whole network.
type Snapshot struct {
	ObservedAt time.Time
	Nodes      []NodeStatus
}

func (s Snapshot) ByNode(id NodeID) (NodeStatus, bool) {
	for _, node := range s.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return NodeStatus{}, false
}

func (s Snapshot) MaxHeight() int64 {
	var max int64
	for _, node := range s.Nodes {
		if node.Reachable && node.Height > max {
			max = node.Height
		}
	}
	return max
}

func (s Snapshot) ReachableCount() int {
	var count int
	for _, node := range s.Nodes {
		if node.Reachable {
			count++
		}
	}
	return count
}

func (s Snapshot) ReadyCount() int {
	var count int
	for _, node := range s.Nodes {
		if node.Reachable && node.Ready {
			count++
		}
	}
	return count
}

func (s Snapshot) ValidatorPower() (totalPower, livePower int64) {
	for _, node := range s.Nodes {
		if node.ValidatorPower <= 0 {
			continue
		}
		totalPower += node.ValidatorPower
		if node.Live {
			livePower += node.ValidatorPower
		}
	}
	return totalPower, livePower
}

func (s Snapshot) HasValidatorQuorum() bool {
	totalPower, livePower := s.ValidatorPower()
	return totalPower > 0 && livePower*3 > totalPower*2
}

func (s Snapshot) Summary() string {
	const maxSummaryNodes = 20

	count := len(s.Nodes)
	if count > maxSummaryNodes {
		count = maxSummaryNodes
	}
	parts := make([]string, 0, count+1)
	for _, node := range s.Nodes[:count] {
		state := fmt.Sprintf("unreachable live=%t power=%d", node.Live, node.ValidatorPower)
		if node.Reachable {
			state = fmt.Sprintf("h=%d ready=%t live=%t power=%d", node.Height, node.Ready, node.Live, node.ValidatorPower)
		}
		parts = append(parts, fmt.Sprintf("%s(%s)", node.ID, state))
	}
	if len(s.Nodes) > maxSummaryNodes {
		parts = append(parts, fmt.Sprintf("... %d more", len(s.Nodes)-maxSummaryNodes))
	}
	return strings.Join(parts, ", ")
}
