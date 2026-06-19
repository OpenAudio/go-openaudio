package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/fuzz"
)

func TestQuorumCount(t *testing.T) {
	tests := map[int]int{
		0:   0,
		1:   1,
		2:   2,
		3:   3,
		4:   3,
		50:  34,
		300: 201,
	}
	for nodes, want := range tests {
		if got := quorumCount(nodes); got != want {
			t.Fatalf("quorumCount(%d) = %d, want %d", nodes, got, want)
		}
	}
}

func TestChaosModeRequiresMutationOptIn(t *testing.T) {
	err := run(context.Background(), []string{"-mode", "chaos"})
	if err == nil {
		t.Fatal("expected chaos mode to require mutation opt-in")
	}
	if !strings.Contains(err.Error(), "-allow-mutations") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSimModeRuns(t *testing.T) {
	err := run(context.Background(), []string{
		"-mode", "sim",
		"-nodes", "5",
		"-steps", "10",
		"-iterations", "1",
		"-live-timeout", "2s",
		"-live-window", "1s",
		"-poll-interval", "1ms",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeSimulatedLoopConfigCapsLiveWindows(t *testing.T) {
	cfg := normalizeSimulatedLoopConfig(simulatedLoopConfig{
		window:       60 * time.Second,
		pollInterval: 2 * time.Second,
	})
	if cfg.window > 100*time.Millisecond {
		t.Fatalf("simulated window was not capped: %s", cfg.window)
	}
	if cfg.pollInterval > cfg.window/10 {
		t.Fatalf("simulated poll interval too slow: window=%s poll=%s", cfg.window, cfg.pollInterval)
	}
}

func TestNodeIDs(t *testing.T) {
	got := nodeIDs([]string{"node1", "", "node300"})
	want := []fuzz.NodeID{"node1", "node300"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
