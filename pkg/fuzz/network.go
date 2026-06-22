package fuzz

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrNodeNotFound    = errors.New("node not found")
	ErrNodeNotManaged  = errors.New("node is not managed by this network")
	ErrNoStatusReader  = errors.New("network has no status reader")
	ErrInvalidScenario = errors.New("invalid scenario")
)

// StatusReader reads externally visible node status.
type StatusReader interface {
	GetNodeStatus(ctx context.Context, node NodeSpec) (NodeStatus, error)
}

// Network is the control and observation surface used by scenarios.
type Network interface {
	Spec() NetworkSpec
	StartNode(ctx context.Context, id NodeID) error
	StopNode(ctx context.Context, id NodeID) error
	RestartNode(ctx context.Context, id NodeID) error
	Snapshot(ctx context.Context) (Snapshot, error)
	Close(ctx context.Context) error
}

// StaticNetwork observes an already-running network. It cannot start or stop
// nodes, but it is useful for running assertions against devnet, staging, or
// production-like endpoints.
type StaticNetwork struct {
	spec   NetworkSpec
	reader StatusReader
}

func NewStaticNetwork(spec NetworkSpec, reader StatusReader) (*StaticNetwork, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if reader == nil {
		return nil, ErrNoStatusReader
	}
	return &StaticNetwork{spec: spec, reader: reader}, nil
}

func (n *StaticNetwork) Spec() NetworkSpec {
	return n.spec
}

func (n *StaticNetwork) StartNode(context.Context, NodeID) error {
	return ErrNodeNotManaged
}

func (n *StaticNetwork) StopNode(context.Context, NodeID) error {
	return ErrNodeNotManaged
}

func (n *StaticNetwork) RestartNode(context.Context, NodeID) error {
	return ErrNodeNotManaged
}

func (n *StaticNetwork) Snapshot(ctx context.Context) (Snapshot, error) {
	return snapshot(ctx, n.spec, n.reader)
}

func (n *StaticNetwork) Close(context.Context) error {
	return nil
}

func snapshot(ctx context.Context, spec NetworkSpec, reader StatusReader) (Snapshot, error) {
	if reader == nil {
		return Snapshot{}, ErrNoStatusReader
	}

	now := time.Now().UTC()
	statuses := make([]NodeStatus, len(spec.Nodes))
	sem := make(chan struct{}, snapshotParallelism(len(spec.Nodes)))
	var wg sync.WaitGroup
	for i, node := range spec.Nodes {
		i, node := i, node
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			status, err := reader.GetNodeStatus(ctx, node)
			if status.ID == "" {
				status.ID = node.ID
			}
			if status.Endpoint == "" {
				status.Endpoint = node.Endpoint
			}
			if status.ObservedAt.IsZero() {
				status.ObservedAt = now
			}
			if err != nil && status.ObservationError == "" {
				status.ObservationError = err.Error()
			}
			statuses[i] = status
		}()
	}
	wg.Wait()

	return Snapshot{
		ObservedAt: now,
		Nodes:      statuses,
	}, nil
}

func snapshotParallelism(nodes int) int {
	switch {
	case nodes <= 0:
		return 1
	case nodes < 32:
		return nodes
	default:
		return 32
	}
}
