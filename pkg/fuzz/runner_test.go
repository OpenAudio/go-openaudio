package fuzz

import (
	"context"
	"testing"
	"time"
)

func TestRunnerRecordsEvents(t *testing.T) {
	reader := newScriptedReader(map[NodeID][]int64{
		"node1": {1, 2},
	})
	network, err := NewStaticNetwork(NetworkSpec{
		Name:  "scripted",
		Nodes: []NodeSpec{{ID: "node1", Endpoint: "http://node1"}},
	}, reader)
	if err != nil {
		t.Fatal(err)
	}

	runner := Runner{
		Network:     network,
		Seed:        7,
		StepTimeout: time.Second,
	}
	result, err := runner.Run(context.Background(), Scenario{
		Name: "records",
		Steps: []Step{
			ActionStep("hook", HookAction("mark", func(context.Context, *RunContext) error {
				return nil
			})),
			AssertionStep("liveness", HeightAdvances(1, time.Second, time.Millisecond)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Fatal("expected passing result")
	}
	if result.Seed != 7 {
		t.Fatalf("unexpected seed %d", result.Seed)
	}
	if len(result.Events) == 0 {
		t.Fatal("expected events to be recorded")
	}
}
