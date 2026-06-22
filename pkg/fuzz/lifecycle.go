package fuzz

import (
	"context"
	"fmt"
)

// Registrar is implemented by test helpers that can register or deregister a
// node through whatever backing system the scenario is using.
type Registrar interface {
	RegisterNode(ctx context.Context, node NodeSpec) error
	DeregisterNode(ctx context.Context, node NodeSpec) error
}

// EndpointMutator is implemented by test helpers that can make a node advertise
// a different endpoint. This is the harness hook for "lying" behaviors.
type EndpointMutator interface {
	SetNodeEndpoint(ctx context.Context, node NodeSpec, endpoint string) error
}

// Jailer is implemented by test helpers that can remove and restore a node
// from the active validator set without deleting the app-level node row.
type Jailer interface {
	JailNode(ctx context.Context, node NodeSpec) error
	UnjailNode(ctx context.Context, node NodeSpec) error
}

func RegisterNodeWith(registrar Registrar, id NodeID) Action {
	return ActionFunc{
		Label: fmt.Sprintf("register %s", id),
		Fn: func(ctx context.Context, run *RunContext) error {
			node, ok := run.Network.Spec().Node(id)
			if !ok {
				return fmt.Errorf("%w: %s", ErrNodeNotFound, id)
			}
			return registrar.RegisterNode(ctx, node)
		},
	}
}

func DeregisterNodeWith(registrar Registrar, id NodeID) Action {
	return ActionFunc{
		Label: fmt.Sprintf("deregister %s", id),
		Fn: func(ctx context.Context, run *RunContext) error {
			node, ok := run.Network.Spec().Node(id)
			if !ok {
				return fmt.Errorf("%w: %s", ErrNodeNotFound, id)
			}
			return registrar.DeregisterNode(ctx, node)
		},
	}
}

func AdvertiseEndpointWith(mutator EndpointMutator, id NodeID, endpoint string) Action {
	return ActionFunc{
		Label: fmt.Sprintf("advertise endpoint for %s", id),
		Fn: func(ctx context.Context, run *RunContext) error {
			node, ok := run.Network.Spec().Node(id)
			if !ok {
				return fmt.Errorf("%w: %s", ErrNodeNotFound, id)
			}
			return mutator.SetNodeEndpoint(ctx, node, endpoint)
		},
	}
}

func JailNodeWith(jailer Jailer, id NodeID) Action {
	return ActionFunc{
		Label: fmt.Sprintf("jail %s", id),
		Fn: func(ctx context.Context, run *RunContext) error {
			node, ok := run.Network.Spec().Node(id)
			if !ok {
				return fmt.Errorf("%w: %s", ErrNodeNotFound, id)
			}
			return jailer.JailNode(ctx, node)
		},
	}
}

func UnjailNodeWith(jailer Jailer, id NodeID) Action {
	return ActionFunc{
		Label: fmt.Sprintf("unjail %s", id),
		Fn: func(ctx context.Context, run *RunContext) error {
			node, ok := run.Network.Spec().Node(id)
			if !ok {
				return fmt.Errorf("%w: %s", ErrNodeNotFound, id)
			}
			return jailer.UnjailNode(ctx, node)
		},
	}
}
