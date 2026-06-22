package fuzz

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLiveLivenessScenario(t *testing.T) {
	if os.Getenv("OPENAUDIO_FUZZ_RUN") != "1" {
		t.Skip("set OPENAUDIO_FUZZ_RUN=1 and OPENAUDIO_FUZZ_ENDPOINTS to run live fuzz checks")
	}

	options := []ClientOption{
		WithDefaultScheme(envDefault("OPENAUDIO_FUZZ_SCHEME", "https")),
	}
	if os.Getenv("OPENAUDIO_FUZZ_INSECURE_TLS") == "1" {
		options = append(options, WithInsecureTLS())
	}
	client := NewClient(options...)

	rawEndpoints := splitCSV(os.Getenv("OPENAUDIO_FUZZ_ENDPOINTS"))
	if len(rawEndpoints) == 0 {
		discoveryEndpoint := os.Getenv("OPENAUDIO_FUZZ_DISCOVERY_ENDPOINT")
		if discoveryEndpoint == "" {
			t.Fatal("OPENAUDIO_FUZZ_ENDPOINTS or OPENAUDIO_FUZZ_DISCOVERY_ENDPOINT is required")
		}
		var err error
		rawEndpoints, err = client.DiscoverValidatorEndpoints(context.Background(), discoveryEndpoint)
		if err != nil {
			t.Fatalf("discover validator endpoints: %v", err)
		}
	}
	if maxEndpoints := envInt("OPENAUDIO_FUZZ_MAX_ENDPOINTS", 0); maxEndpoints > 0 && len(rawEndpoints) > maxEndpoints {
		rawEndpoints = rawEndpoints[:maxEndpoints]
	}

	nodes := make([]NodeSpec, 0, len(rawEndpoints))
	for i, endpoint := range rawEndpoints {
		nodes = append(nodes, NodeSpec{
			ID:       NodeID(fmt.Sprintf("node%d", i+1)),
			Endpoint: endpoint,
		})
	}

	network, err := NewStaticNetwork(NetworkSpec{
		Name:  "live",
		Nodes: nodes,
	}, client)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	minReachable := envInt("OPENAUDIO_FUZZ_MIN_REACHABLE", len(nodes))
	result, err := Runner{
		Network:     network,
		Seed:        time.Now().UnixNano(),
		StepTimeout: 90 * time.Second,
	}.Run(ctx, LiveLivenessScenario(minReachable, 1, 60*time.Second, 2*time.Second))
	if err != nil {
		t.Fatalf("scenario failed after %d events: %v", len(result.Events), err)
	}
}

func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
