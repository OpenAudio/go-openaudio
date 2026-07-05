package fuzz

import (
	"context"
	"testing"
)

type fakeLifecycleController struct {
	registered   []NodeID
	deregistered []NodeID
	endpoints    map[NodeID]string
}

func (f *fakeLifecycleController) RegisterNode(_ context.Context, node NodeSpec) error {
	f.registered = append(f.registered, node.ID)
	return nil
}

func (f *fakeLifecycleController) DeregisterNode(_ context.Context, node NodeSpec) error {
	f.deregistered = append(f.deregistered, node.ID)
	return nil
}

func (f *fakeLifecycleController) SetNodeEndpoint(_ context.Context, node NodeSpec, endpoint string) error {
	if f.endpoints == nil {
		f.endpoints = map[NodeID]string{}
	}
	f.endpoints[node.ID] = endpoint
	return nil
}

func TestLifecycleActions(t *testing.T) {
	reader := newScriptedReader(map[NodeID][]int64{
		"node1": {1},
	})
	network, err := NewStaticNetwork(NetworkSpec{
		Name:  "scripted",
		Nodes: []NodeSpec{{ID: "node1", Endpoint: "https://node1.oap.devnet"}},
	}, reader)
	if err != nil {
		t.Fatal(err)
	}

	controller := &fakeLifecycleController{}
	_, err = Runner{Network: network}.Run(context.Background(), Scenario{
		Name: "lifecycle",
		Steps: []Step{
			ActionStep("register", RegisterNodeWith(controller, "node1")),
			ActionStep("lie", AdvertiseEndpointWith(controller, "node1", "https://wrong.oap.devnet")),
			ActionStep("deregister", DeregisterNodeWith(controller, "node1")),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(controller.registered) != 1 || controller.registered[0] != "node1" {
		t.Fatalf("unexpected registrations: %v", controller.registered)
	}
	if controller.endpoints["node1"] != "https://wrong.oap.devnet" {
		t.Fatalf("unexpected endpoint mutations: %v", controller.endpoints)
	}
	if len(controller.deregistered) != 1 || controller.deregistered[0] != "node1" {
		t.Fatalf("unexpected deregistrations: %v", controller.deregistered)
	}
}
