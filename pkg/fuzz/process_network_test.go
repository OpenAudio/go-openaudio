package fuzz

import (
	"context"
	"testing"
	"time"
)

func TestProcessNetworkStartOutlivesStepContext(t *testing.T) {
	reader := newScriptedReader(map[NodeID][]int64{
		"node1": {1},
	})
	network, err := NewProcessNetwork(NetworkSpec{
		Name: "process",
		Nodes: []NodeSpec{
			{
				ID:       "node1",
				Endpoint: "http://node1",
				Command:  []string{"sleep", "60"},
			},
		},
	}, reader)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := network.Close(ctx); err != nil {
			t.Fatalf("close process network: %v", err)
		}
	}()

	_, err = Runner{
		Network:     network,
		StepTimeout: 50 * time.Millisecond,
	}.Run(context.Background(), Scenario{
		Name:  "process lifetime",
		Steps: []Step{ActionStep("start", StartNode("node1"))},
	})
	if err != nil {
		t.Fatal(err)
	}

	network.mu.Lock()
	proc := network.processes["node1"]
	network.mu.Unlock()
	if proc == nil {
		t.Fatal("expected managed process to still be tracked")
	}

	select {
	case err := <-proc.done:
		t.Fatalf("process exited when step context ended: %v", err)
	default:
	}
}
