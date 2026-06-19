package fuzz

import "time"

func BasicLivenessScenario(minHeightDelta int64, within, pollInterval time.Duration) Scenario {
	return Scenario{
		Name: "basic-liveness",
		Steps: []Step{
			AssertionStep("all nodes reachable", AllReachable()),
			AssertionStep("height advances", HeightAdvances(minHeightDelta, within, pollInterval)),
			AssertionStep("height does not regress", NoHeightRegression(within, pollInterval)),
		},
	}
}

func LiveLivenessScenario(requiredReachable int, minHeightDelta int64, within, pollInterval time.Duration) Scenario {
	regressionWindow := 2 * pollInterval
	if regressionWindow <= 0 {
		regressionWindow = 2 * defaultPollInterval
	}
	return Scenario{
		Name: "live-liveness",
		Steps: []Step{
			AssertionStep("reachable quorum", ReachableAtLeast(requiredReachable, within, pollInterval)),
			AssertionStep("height advances", HeightAdvances(minHeightDelta, within, pollInterval)),
			AssertionStep("height does not regress", NoHeightRegression(regressionWindow, pollInterval)),
		},
	}
}

func RestartNodeScenario(id NodeID, readyCount int, within, pollInterval time.Duration) Scenario {
	return Scenario{
		Name: "restart-node",
		Steps: []Step{
			ActionStep("restart node", RestartNode(id)),
			AssertionStep("quorum ready", QuorumReady(readyCount, within, pollInterval)),
			AssertionStep("height advances", HeightAdvances(1, within, pollInterval)),
		},
	}
}
