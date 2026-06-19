package fuzz

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type VersionSkewMode int

const (
	VersionSkewModeHaltLegacyOnJailedDeregister VersionSkewMode = iota
	VersionSkewModeForkLegacyOnJailedDeregister
	VersionSkewModeHaltLegacyOnDuplicateDeregister
	VersionSkewModeForkLegacyOnDuplicateDeregister
	VersionSkewModeHaltLegacyOnEndpointLie
	VersionSkewModeForkLegacyOnEndpointLie
	VersionSkewModeHaltLegacyOnRegister
	VersionSkewModeForkLegacyOnRegister
	VersionSkewModeNoopOnUnjail
	VersionSkewModeHaltLegacyOnUnjail
	VersionSkewModeForkLegacyOnUnjail
)

type VersionSkewNetworkOptions struct {
	Name          string
	NodeCount     int
	LegacyNodeIDs []NodeID
	Mode          VersionSkewMode
	NodePowers    map[NodeID]int64
}

type VersionSkewNetwork struct {
	mu                sync.Mutex
	spec              NetworkSpec
	nodes             map[NodeID]*versionSkewNode
	legacy            map[NodeID]struct{}
	originalEndpoints map[NodeID]string
	currentEndpoints  map[NodeID]string
	mode              VersionSkewMode
	height            int64
	diverged          bool
}

type versionSkewNode struct {
	id             NodeID
	state          ModelValidatorState
	online         bool
	endpointHonest bool
	power          int64
}

func NewVersionSkewNetwork(opts VersionSkewNetworkOptions) (*VersionSkewNetwork, error) {
	nodeCount := clamp(opts.NodeCount, 1, DefaultModelNodeLimit)
	name := opts.Name
	if name == "" {
		name = "version-skew"
	}

	spec := NetworkSpec{Name: name, Nodes: make([]NodeSpec, 0, nodeCount)}
	nodes := make(map[NodeID]*versionSkewNode, nodeCount)
	originalEndpoints := make(map[NodeID]string, nodeCount)
	currentEndpoints := make(map[NodeID]string, nodeCount)
	for i := 0; i < nodeCount; i++ {
		id := NodeID(fmt.Sprintf("node%d", i+1))
		endpoint := fmt.Sprintf("https://%s.oap.version-skew", id)
		power := opts.NodePowers[id]
		if power <= 0 {
			power = defaultModelNodePower
		}
		spec.Nodes = append(spec.Nodes, NodeSpec{ID: id, Endpoint: endpoint})
		nodes[id] = &versionSkewNode{
			id:             id,
			state:          ModelValidatorActive,
			online:         true,
			endpointHonest: true,
			power:          power,
		}
		originalEndpoints[id] = endpoint
		currentEndpoints[id] = endpoint
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	legacy := make(map[NodeID]struct{}, len(opts.LegacyNodeIDs))
	for _, id := range opts.LegacyNodeIDs {
		if _, ok := nodes[id]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrNodeNotFound, id)
		}
		legacy[id] = struct{}{}
	}
	if len(legacy) == 0 {
		legacy[spec.Nodes[0].ID] = struct{}{}
	}

	return &VersionSkewNetwork{
		spec:              spec,
		nodes:             nodes,
		legacy:            legacy,
		originalEndpoints: originalEndpoints,
		currentEndpoints:  currentEndpoints,
		mode:              opts.Mode,
	}, nil
}

func (n *VersionSkewNetwork) Spec() NetworkSpec {
	return n.spec
}

func (n *VersionSkewNetwork) StartNode(ctx context.Context, id NodeID) error {
	return n.withNode(ctx, id, func(node *versionSkewNode) {
		if node.state == ModelValidatorActive {
			node.online = true
		}
	})
}

func (n *VersionSkewNetwork) StopNode(ctx context.Context, id NodeID) error {
	return n.withNode(ctx, id, func(node *versionSkewNode) {
		node.online = false
	})
}

func (n *VersionSkewNetwork) RestartNode(ctx context.Context, id NodeID) error {
	if err := n.StopNode(ctx, id); err != nil {
		return err
	}
	return n.StartNode(ctx, id)
}

func (n *VersionSkewNetwork) RegisterNode(ctx context.Context, node NodeSpec) error {
	if err := n.withNode(ctx, node.ID, func(modelNode *versionSkewNode) {
		if modelNode.state == ModelValidatorAbsent {
			n.triggerRegisterIncompatibilityLocked()
		}
		modelNode.state = ModelValidatorActive
		modelNode.online = true
		modelNode.endpointHonest = true
	}); err != nil {
		return err
	}

	n.mu.Lock()
	n.currentEndpoints[node.ID] = n.originalEndpoints[node.ID]
	n.mu.Unlock()
	return nil
}

func (n *VersionSkewNetwork) DeregisterNode(ctx context.Context, node NodeSpec) error {
	return n.withNode(ctx, node.ID, func(modelNode *versionSkewNode) {
		switch modelNode.state {
		case ModelValidatorJailed:
			n.triggerJailedDeregisterIncompatibilityLocked()
		case ModelValidatorAbsent:
			n.triggerDuplicateDeregisterIncompatibilityLocked()
		}
		modelNode.state = ModelValidatorAbsent
		modelNode.online = false
		modelNode.endpointHonest = true
	})
}

func (n *VersionSkewNetwork) SetNodeEndpoint(ctx context.Context, node NodeSpec, endpoint string) error {
	if err := n.withNode(ctx, node.ID, func(modelNode *versionSkewNode) {
		original := n.originalEndpoints[node.ID]
		honest := endpoint == "" || endpoint == original
		if !honest {
			n.triggerEndpointLieIncompatibilityLocked()
		}
		modelNode.endpointHonest = honest
	}); err != nil {
		return err
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if endpoint == "" {
		n.currentEndpoints[node.ID] = n.originalEndpoints[node.ID]
	} else {
		n.currentEndpoints[node.ID] = endpoint
	}
	return nil
}

func (n *VersionSkewNetwork) JailNode(ctx context.Context, node NodeSpec) error {
	return n.withNode(ctx, node.ID, func(modelNode *versionSkewNode) {
		if modelNode.state == ModelValidatorActive {
			modelNode.state = ModelValidatorJailed
			modelNode.online = false
		}
	})
}

func (n *VersionSkewNetwork) UnjailNode(ctx context.Context, node NodeSpec) error {
	return n.withNode(ctx, node.ID, func(modelNode *versionSkewNode) {
		if modelNode.state == ModelValidatorJailed {
			if n.mode == VersionSkewModeNoopOnUnjail {
				return
			}
			n.triggerUnjailIncompatibilityLocked()
			modelNode.state = ModelValidatorActive
			modelNode.online = true
		}
	})
}

func (n *VersionSkewNetwork) Snapshot(ctx context.Context) (Snapshot, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if n.hasLiveQuorumLocked() {
		n.height++
	}

	now := time.Now().UTC()
	statuses := make([]NodeStatus, 0, len(n.spec.Nodes))
	for _, spec := range n.spec.Nodes {
		node := n.nodes[spec.ID]
		live := node.state == ModelValidatorActive && node.online
		reachable := live && node.endpointHonest
		statuses = append(statuses, NodeStatus{
			ID:             spec.ID,
			Endpoint:       n.currentEndpoints[spec.ID],
			Reachable:      reachable,
			Ready:          reachable,
			Live:           live,
			Synced:         live,
			Height:         n.height,
			BlockHash:      n.blockHashLocked(spec.ID),
			Version:        n.versionLocked(spec.ID),
			ValidatorPower: versionSkewValidatorPower(node),
			ObservedAt:     now,
		})
	}
	return Snapshot{ObservedAt: now, Nodes: statuses}, nil
}

func (n *VersionSkewNetwork) Close(context.Context) error {
	return nil
}

func (n *VersionSkewNetwork) withNode(ctx context.Context, id NodeID, fn func(*versionSkewNode)) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	node := n.nodes[id]
	if node == nil {
		return fmt.Errorf("%w: %s", ErrNodeNotFound, id)
	}
	fn(node)
	return nil
}

func (n *VersionSkewNetwork) triggerJailedDeregisterIncompatibilityLocked() {
	switch n.mode {
	case VersionSkewModeForkLegacyOnJailedDeregister:
		n.diverged = true
	case VersionSkewModeHaltLegacyOnJailedDeregister:
		for id := range n.legacy {
			if node := n.nodes[id]; node != nil && node.state == ModelValidatorActive {
				node.online = false
			}
		}
	}
}

func (n *VersionSkewNetwork) triggerDuplicateDeregisterIncompatibilityLocked() {
	switch n.mode {
	case VersionSkewModeForkLegacyOnDuplicateDeregister:
		n.diverged = true
	case VersionSkewModeHaltLegacyOnDuplicateDeregister:
		for id := range n.legacy {
			if node := n.nodes[id]; node != nil && node.state == ModelValidatorActive {
				node.online = false
			}
		}
	}
}

func (n *VersionSkewNetwork) triggerEndpointLieIncompatibilityLocked() {
	switch n.mode {
	case VersionSkewModeForkLegacyOnEndpointLie:
		n.diverged = true
	case VersionSkewModeHaltLegacyOnEndpointLie:
		for id := range n.legacy {
			if node := n.nodes[id]; node != nil && node.state == ModelValidatorActive {
				node.online = false
			}
		}
	}
}

func (n *VersionSkewNetwork) triggerRegisterIncompatibilityLocked() {
	switch n.mode {
	case VersionSkewModeForkLegacyOnRegister:
		n.diverged = true
	case VersionSkewModeHaltLegacyOnRegister:
		for id := range n.legacy {
			if node := n.nodes[id]; node != nil && node.state == ModelValidatorActive {
				node.online = false
			}
		}
	}
}

func (n *VersionSkewNetwork) triggerUnjailIncompatibilityLocked() {
	switch n.mode {
	case VersionSkewModeForkLegacyOnUnjail:
		n.diverged = true
	case VersionSkewModeHaltLegacyOnUnjail:
		for id := range n.legacy {
			if node := n.nodes[id]; node != nil && node.state == ModelValidatorActive {
				node.online = false
			}
		}
	}
}

func (n *VersionSkewNetwork) hasLiveQuorumLocked() bool {
	totalPower, livePower := n.validatorPowerLocked()
	return totalPower > 0 && livePower*3 > totalPower*2
}

func (n *VersionSkewNetwork) validatorPowerLocked() (totalPower, livePower int64) {
	for _, node := range n.nodes {
		power := versionSkewValidatorPower(node)
		if power <= 0 {
			continue
		}
		totalPower += power
		if node.online {
			livePower += power
		}
	}
	return totalPower, livePower
}

func (n *VersionSkewNetwork) blockHashLocked(id NodeID) string {
	if n.height <= 0 {
		return ""
	}
	hash := simulatedBlockHash(n.height)
	if n.diverged {
		if _, ok := n.legacy[id]; ok {
			return "legacy-" + hash
		}
	}
	return hash
}

func (n *VersionSkewNetwork) versionLocked(id NodeID) string {
	if _, ok := n.legacy[id]; ok {
		return "legacy"
	}
	return "current"
}

func versionSkewValidatorPower(node *versionSkewNode) int64 {
	if node.state != ModelValidatorActive {
		return 0
	}
	return node.power
}
