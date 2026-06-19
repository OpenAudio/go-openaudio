package fuzz

import (
	"fmt"
	"time"
)

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

func QuorumLossRecoveryScenario(spec NetworkSpec, within, pollInterval time.Duration) Scenario {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	if within <= 0 {
		within = 30 * time.Second
	}
	stepTimeout := within + pollInterval + time.Second
	regressionWindow := 2 * pollInterval
	if regressionWindow <= 0 {
		regressionWindow = 2 * defaultPollInterval
	}

	cohort := quorumLossCohort(spec.NodeIDs())
	stop := make([]Action, 0, len(cohort))
	start := make([]Action, 0, len(cohort))
	for _, id := range cohort {
		stop = append(stop, StopNode(id))
		start = append(start, StartNode(id))
	}

	return Scenario{
		Name: "quorum-loss-recovery",
		Steps: []Step{
			{
				Name:       "initial height advances",
				Assertions: []Assertion{HeightAdvances(1, within, pollInterval)},
				Timeout:    stepTimeout,
			},
			{
				Name:    fmt.Sprintf("stop quorum-loss cohort %d nodes", len(cohort)),
				Actions: []Action{Parallel("stop quorum-loss cohort", stop...)},
				Timeout: stepTimeout,
			},
			{
				Name:       "height stalls without quorum",
				Assertions: []Assertion{HeightStalls(within, pollInterval)},
				Timeout:    stepTimeout,
			},
			{
				Name:    fmt.Sprintf("restart quorum-loss cohort %d nodes", len(cohort)),
				Actions: []Action{Parallel("restart quorum-loss cohort", start...)},
				Timeout: stepTimeout,
			},
			{
				Name:       "height recovers",
				Assertions: []Assertion{HeightAdvances(1, within, pollInterval), NoHeightRegression(regressionWindow, pollInterval)},
				Timeout:    stepTimeout,
			},
		},
	}
}

func quorumLossCohort(ids []NodeID) []NodeID {
	if len(ids) == 0 {
		return nil
	}
	size := len(ids) - (len(ids)*2)/3
	if size < 1 {
		size = 1
	}
	if size > len(ids) {
		size = len(ids)
	}
	return append([]NodeID{}, ids[:size]...)
}
