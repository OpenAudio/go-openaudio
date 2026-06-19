package fuzz

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// SimulatedNetwork is an in-memory Network plus lifecycle controller. It runs
// the same chaos scenarios as a real network but keeps everything inside Go,
// which makes high-volume 300-node loops practical on a laptop.
type SimulatedNetwork struct {
	mu                sync.Mutex
	spec              NetworkSpec
	model             *ValidatorLifecycleModel
	originalEndpoints map[NodeID]string
	currentEndpoints  map[NodeID]string
	step              int
	tickOnSnapshot    bool
}

type SimulatedNetworkOptions struct {
	Name           string
	NodeCount      int
	InitialActive  int
	Behavior       ValidatorSetBehavior
	TickOnSnapshot bool
}

func NewSimulatedNetwork(opts SimulatedNetworkOptions) (*SimulatedNetwork, error) {
	nodeCount := clamp(opts.NodeCount, 1, DefaultModelNodeLimit)
	name := opts.Name
	if name == "" {
		name = "simulated"
	}

	spec := NetworkSpec{Name: name, Nodes: make([]NodeSpec, 0, nodeCount)}
	originalEndpoints := make(map[NodeID]string, nodeCount)
	currentEndpoints := make(map[NodeID]string, nodeCount)
	for i := 0; i < nodeCount; i++ {
		id := NodeID(fmt.Sprintf("node%d", i+1))
		endpoint := fmt.Sprintf("https://%s.oap.simulated", id)
		spec.Nodes = append(spec.Nodes, NodeSpec{ID: id, Endpoint: endpoint})
		originalEndpoints[id] = endpoint
		currentEndpoints[id] = endpoint
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	behavior := opts.Behavior
	model := NewValidatorLifecycleModel(ValidatorModelOptions{
		NodeCount:     nodeCount,
		InitialActive: opts.InitialActive,
		Behavior:      behavior,
	})
	return &SimulatedNetwork{
		spec:              spec,
		model:             model,
		originalEndpoints: originalEndpoints,
		currentEndpoints:  currentEndpoints,
		tickOnSnapshot:    opts.TickOnSnapshot,
	}, nil
}

func (n *SimulatedNetwork) Spec() NetworkSpec {
	return n.spec
}

func (n *SimulatedNetwork) StartNode(ctx context.Context, id NodeID) error {
	return n.apply(ctx, ModelAction{Kind: ModelStart, Node: id})
}

func (n *SimulatedNetwork) StopNode(ctx context.Context, id NodeID) error {
	return n.apply(ctx, ModelAction{Kind: ModelStop, Node: id})
}

func (n *SimulatedNetwork) RestartNode(ctx context.Context, id NodeID) error {
	if err := n.StopNode(ctx, id); err != nil {
		return err
	}
	return n.StartNode(ctx, id)
}

func (n *SimulatedNetwork) RegisterNode(ctx context.Context, node NodeSpec) error {
	if err := n.apply(ctx, ModelAction{Kind: ModelRegister, Node: node.ID}); err != nil {
		return err
	}
	n.mu.Lock()
	n.currentEndpoints[node.ID] = n.originalEndpoints[node.ID]
	n.mu.Unlock()
	return nil
}

func (n *SimulatedNetwork) DeregisterNode(ctx context.Context, node NodeSpec) error {
	return n.apply(ctx, ModelAction{Kind: ModelDeregister, Node: node.ID})
}

func (n *SimulatedNetwork) SetNodeEndpoint(ctx context.Context, node NodeSpec, endpoint string) error {
	original := n.originalEndpoints[node.ID]
	if endpoint == "" || endpoint == original {
		if err := n.apply(ctx, ModelAction{Kind: ModelRepairEndpoint, Node: node.ID}); err != nil {
			return err
		}
		n.mu.Lock()
		n.currentEndpoints[node.ID] = original
		n.mu.Unlock()
		return nil
	}

	if err := n.apply(ctx, ModelAction{Kind: ModelLieEndpoint, Node: node.ID}); err != nil {
		return err
	}
	n.mu.Lock()
	n.currentEndpoints[node.ID] = endpoint
	n.mu.Unlock()
	return nil
}

func (n *SimulatedNetwork) JailNode(ctx context.Context, node NodeSpec) error {
	return n.apply(ctx, ModelAction{Kind: ModelJail, Node: node.ID})
}

func (n *SimulatedNetwork) UnjailNode(ctx context.Context, node NodeSpec) error {
	return n.apply(ctx, ModelAction{Kind: ModelUnjail, Node: node.ID})
}

func (n *SimulatedNetwork) Snapshot(ctx context.Context) (Snapshot, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if n.tickOnSnapshot {
		if err := n.applyLocked(ModelAction{Kind: ModelTick}); err != nil {
			return Snapshot{}, err
		}
	}

	now := time.Now().UTC()
	statuses := make([]NodeStatus, 0, len(n.spec.Nodes))
	for _, spec := range n.spec.Nodes {
		node := n.model.Nodes[spec.ID]
		if node == nil {
			return Snapshot{}, fmt.Errorf("%w: %s", ErrNodeNotFound, spec.ID)
		}
		reachable := node.State == ModelValidatorActive && node.Online && node.EndpointHonest
		statuses = append(statuses, NodeStatus{
			ID:             spec.ID,
			Endpoint:       n.currentEndpoints[spec.ID],
			Reachable:      reachable,
			Ready:          reachable,
			Live:           reachable,
			Synced:         reachable,
			Height:         n.model.Height,
			ValidatorPower: validatorPower(node),
			ObservedAt:     now,
		})
	}
	return Snapshot{ObservedAt: now, Nodes: statuses}, nil
}

func (n *SimulatedNetwork) Close(context.Context) error {
	return nil
}

func (n *SimulatedNetwork) apply(ctx context.Context, action ModelAction) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.applyLocked(action)
}

func (n *SimulatedNetwork) applyLocked(action ModelAction) error {
	n.step++
	return n.model.Apply(n.step, action)
}

func validatorPower(node *ModelNode) int64 {
	if node.InCometSet {
		return node.Power
	}
	return 0
}
