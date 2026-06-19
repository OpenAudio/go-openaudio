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

func OutcomeEdgeCaseScenario(spec NetworkSpec, controller ValidatorChaosController, within, pollInterval time.Duration) Scenario {
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
	progressAssertions := []Assertion{
		HeightAdvances(1, within, pollInterval),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	ids := spec.NodeIDs()
	scenario := Scenario{
		Name: "outcome-edge-cases",
		Steps: []Step{
			{
				Name:       "initial height advances",
				Assertions: progressAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 {
		return scenario
	}

	first := ids[0]
	if minimumQuorumNodes(len(ids)) < len(ids) {
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep("stop one node; chain still progresses", stepTimeout, []Action{StopNode(first)}, progressAssertions),
			outcomeActionStep("restart one node; chain still progresses", stepTimeout, []Action{StartNode(first)}, progressAssertions),
		)
	}

	if cohort := quorumPreservingCohort(ids); len(cohort) > 1 {
		stop, start := stopStartActions(cohort)
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep(fmt.Sprintf("stop quorum-preserving cohort %d nodes; chain still progresses", len(cohort)), stepTimeout, stop, progressAssertions),
			outcomeActionStep(fmt.Sprintf("restart quorum-preserving cohort %d nodes; chain still progresses", len(cohort)), stepTimeout, start, progressAssertions),
		)
	}

	if controller.EndpointMutator != nil && len(ids) > 1 {
		badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", first)
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep("advertise bad endpoint; chain still progresses", stepTimeout, []Action{AdvertiseEndpointWith(controller.EndpointMutator, first, badEndpoint)}, progressAssertions),
			outcomeActionStep("repair endpoint; chain still progresses", stepTimeout, []Action{AdvertiseEndpointWith(controller.EndpointMutator, first, "")}, progressAssertions),
		)
	}

	if controller.Jailer != nil && len(ids) > 1 {
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep("jail one validator; chain still progresses", stepTimeout, []Action{JailNodeWith(controller.Jailer, first)}, progressAssertions),
			outcomeActionStep("unjail one validator; chain still progresses", stepTimeout, []Action{UnjailNodeWith(controller.Jailer, first)}, progressAssertions),
		)
	}

	if controller.Registrar != nil && len(ids) > 1 {
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep("deregister one validator; chain still progresses", stepTimeout, []Action{DeregisterNodeWith(controller.Registrar, first)}, progressAssertions),
			outcomeActionStep("duplicate deregister; chain still progresses", stepTimeout, []Action{DeregisterNodeWith(controller.Registrar, first)}, progressAssertions),
			outcomeActionStep("register one validator; chain still progresses", stepTimeout, []Action{RegisterNodeWith(controller.Registrar, first)}, progressAssertions),
		)
	}

	if controller.Jailer != nil && controller.Registrar != nil && len(ids) > 1 {
		last := ids[len(ids)-1]
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep("jail then deregister validator; chain still progresses", stepTimeout, []Action{
				JailNodeWith(controller.Jailer, last),
				DeregisterNodeWith(controller.Registrar, last),
			}, progressAssertions),
			outcomeActionStep("register jailed-then-deregistered validator; chain still progresses", stepTimeout, []Action{RegisterNodeWith(controller.Registrar, last)}, progressAssertions),
		)
	}

	if loss := quorumLossCohort(ids); len(loss) > 0 {
		stop, start := stopStartActions(loss)
		scenario.Steps = append(scenario.Steps,
			Step{
				Name:       fmt.Sprintf("stop quorum-loss cohort %d nodes; chain stalls", len(loss)),
				Actions:    []Action{Parallel("stop quorum-loss cohort", stop...)},
				Assertions: []Assertion{HeightStalls(within, pollInterval)},
				Timeout:    stepTimeout,
			},
			outcomeActionStep(fmt.Sprintf("restart quorum-loss cohort %d nodes; chain recovers", len(loss)), stepTimeout, start, progressAssertions),
		)
	}

	return scenario
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

func outcomeActionStep(name string, timeout time.Duration, actions []Action, assertions []Assertion) Step {
	return Step{
		Name:       name,
		Actions:    actions,
		Assertions: assertions,
		Timeout:    timeout,
	}
}

func stopStartActions(ids []NodeID) ([]Action, []Action) {
	stop := make([]Action, 0, len(ids))
	start := make([]Action, 0, len(ids))
	for _, id := range ids {
		stop = append(stop, StopNode(id))
		start = append(start, StartNode(id))
	}
	return stop, start
}

func quorumPreservingCohort(ids []NodeID) []NodeID {
	size := len(ids) - minimumQuorumNodes(len(ids))
	if size <= 0 {
		return nil
	}
	return append([]NodeID{}, ids[:size]...)
}

func minimumQuorumNodes(nodes int) int {
	if nodes <= 0 {
		return 0
	}
	return (nodes*2)/3 + 1
}
