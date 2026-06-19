package fuzz

import (
	"context"
	"fmt"
	"sort"
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
	quorumOutcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	ids := spec.NodeIDs()
	scenario := Scenario{
		Name: "outcome-edge-cases",
		Steps: []Step{
			{
				Name:       "initial height advances",
				Assertions: quorumOutcomeAssertions,
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
			outcomeActionStep("stop one node; chain still progresses", stepTimeout, []Action{StopNode(first)}, quorumOutcomeAssertions),
			outcomeActionStep("restart one node; chain still progresses", stepTimeout, []Action{StartNode(first)}, quorumOutcomeAssertions),
		)
	}

	if cohort := quorumPreservingCohort(ids); len(cohort) > 1 {
		stop, start := stopStartActions(cohort)
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep(fmt.Sprintf("stop quorum-preserving cohort %d nodes; chain still progresses", len(cohort)), stepTimeout, stop, quorumOutcomeAssertions),
			outcomeActionStep(fmt.Sprintf("restart quorum-preserving cohort %d nodes; chain still progresses", len(cohort)), stepTimeout, start, quorumOutcomeAssertions),
		)
	}

	if controller.EndpointMutator != nil && len(ids) > 1 {
		badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", first)
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep("advertise bad endpoint; chain still progresses", stepTimeout, []Action{AdvertiseEndpointWith(controller.EndpointMutator, first, badEndpoint)}, quorumOutcomeAssertions),
			outcomeActionStep("repair endpoint; chain still progresses", stepTimeout, []Action{AdvertiseEndpointWith(controller.EndpointMutator, first, "")}, quorumOutcomeAssertions),
		)
	}

	if controller.Jailer != nil && len(ids) > 1 {
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep("jail one validator; chain still progresses", stepTimeout, []Action{JailNodeWith(controller.Jailer, first)}, quorumOutcomeAssertions),
			outcomeActionStep("unjail one validator; chain still progresses", stepTimeout, []Action{UnjailNodeWith(controller.Jailer, first)}, quorumOutcomeAssertions),
		)
	}

	if controller.Registrar != nil && len(ids) > 1 {
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep("deregister one validator; chain still progresses", stepTimeout, []Action{DeregisterNodeWith(controller.Registrar, first)}, quorumOutcomeAssertions),
			outcomeActionStep("duplicate deregister; chain still progresses", stepTimeout, []Action{DeregisterNodeWith(controller.Registrar, first)}, quorumOutcomeAssertions),
			outcomeActionStep("register one validator; chain still progresses", stepTimeout, []Action{RegisterNodeWith(controller.Registrar, first)}, quorumOutcomeAssertions),
		)
	}

	if controller.Jailer != nil && controller.Registrar != nil && len(ids) > 1 {
		last := ids[len(ids)-1]
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep("jail then deregister validator; chain still progresses", stepTimeout, []Action{
				JailNodeWith(controller.Jailer, last),
				DeregisterNodeWith(controller.Registrar, last),
			}, quorumOutcomeAssertions),
			outcomeActionStep("register jailed-then-deregistered validator; chain still progresses", stepTimeout, []Action{RegisterNodeWith(controller.Registrar, last)}, quorumOutcomeAssertions),
		)
	}

	if loss := quorumLossCohort(ids); len(loss) > 0 {
		stop, start := stopStartActions(loss)
		scenario.Steps = append(scenario.Steps,
			Step{
				Name:       fmt.Sprintf("stop quorum-loss cohort %d nodes; chain stalls", len(loss)),
				Actions:    []Action{Parallel("stop quorum-loss cohort", stop...)},
				Assertions: []Assertion{HeightFollowsValidatorQuorum(within, pollInterval)},
				Timeout:    stepTimeout,
			},
			outcomeActionStep(fmt.Sprintf("restart quorum-loss cohort %d nodes; chain recovers", len(loss)), stepTimeout, start, quorumOutcomeAssertions),
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
				Assertions: []Assertion{HeightFollowsValidatorQuorum(within, pollInterval)},
				Timeout:    stepTimeout,
			},
			{
				Name:    fmt.Sprintf("stop quorum-loss cohort %d nodes", len(cohort)),
				Actions: []Action{Parallel("stop quorum-loss cohort", stop...)},
				Timeout: stepTimeout,
			},
			{
				Name:       "height stalls without quorum",
				Assertions: []Assertion{HeightFollowsValidatorQuorum(within, pollInterval)},
				Timeout:    stepTimeout,
			},
			{
				Name:    fmt.Sprintf("restart quorum-loss cohort %d nodes", len(cohort)),
				Actions: []Action{Parallel("restart quorum-loss cohort", start...)},
				Timeout: stepTimeout,
			},
			{
				Name:       "height recovers",
				Assertions: []Assertion{HeightFollowsValidatorQuorum(within, pollInterval), LiveValidatorHeightsConverge(0, within, pollInterval), NoLiveValidatorFork(), NoHeightRegression(regressionWindow, pollInterval)},
				Timeout:    stepTimeout,
			},
		},
	}
}

func JailedDeregisterCompatibilityScenario(spec NetworkSpec, controller ValidatorChaosController, target NodeID, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	if target == "" && len(ids) > 0 {
		target = ids[len(ids)-1]
	}
	postJailBaseline := &ValidatorPowerBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	compatibilityAssertions := []Assertion{
		ValidatorPowerRestored(postJailBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "jailed-deregister-compatibility",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 || target == "" || controller.Jailer == nil || controller.Registrar == nil {
		return scenario
	}

	scenario.Steps = append(scenario.Steps,
		outcomeActionStep("jail validator; chain follows updated set", stepTimeout, []Action{JailNodeWith(controller.Jailer, target)}, outcomeAssertions),
		ActionStep("capture post-jail validator power baseline", CaptureValidatorPowerBaseline(postJailBaseline)),
		Step{
			Name:       "deregister already-jailed validator; chain keeps same validator outcome",
			Actions:    []Action{DeregisterNodeWith(controller.Registrar, target)},
			Assertions: compatibilityAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func DuplicateDeregisterIdempotencyScenario(spec NetworkSpec, controller ValidatorChaosController, target NodeID, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	if target == "" && len(ids) > 0 {
		target = ids[len(ids)-1]
	}
	postDeregisterBaseline := &ValidatorPowerBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	idempotencyAssertions := []Assertion{
		ValidatorPowerRestored(postDeregisterBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "duplicate-deregister-idempotency",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 || target == "" || controller.Registrar == nil {
		return scenario
	}

	scenario.Steps = append(scenario.Steps,
		outcomeActionStep("deregister validator; chain follows updated set", stepTimeout, []Action{DeregisterNodeWith(controller.Registrar, target)}, outcomeAssertions),
		ActionStep("capture post-deregister validator power baseline", CaptureValidatorPowerBaseline(postDeregisterBaseline)),
		Step{
			Name:       "duplicate deregister; chain keeps same validator outcome",
			Actions:    []Action{DeregisterNodeWith(controller.Registrar, target)},
			Assertions: idempotencyAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func DuplicateJailIdempotencyScenario(spec NetworkSpec, controller ValidatorChaosController, target NodeID, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	if target == "" && len(ids) > 0 {
		target = ids[len(ids)-1]
	}
	postJailBaseline := &ValidatorPowerBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	idempotencyAssertions := []Assertion{
		ValidatorPowerRestored(postJailBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "duplicate-jail-idempotency",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 || target == "" || controller.Jailer == nil {
		return scenario
	}

	scenario.Steps = append(scenario.Steps,
		outcomeActionStep("jail validator; chain follows updated set", stepTimeout, []Action{JailNodeWith(controller.Jailer, target)}, outcomeAssertions),
		ActionStep("capture post-jail validator power baseline", CaptureValidatorPowerBaseline(postJailBaseline)),
		Step{
			Name:       "duplicate jail; chain keeps same validator outcome",
			Actions:    []Action{JailNodeWith(controller.Jailer, target)},
			Assertions: idempotencyAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func EndpointLieConsensusIsolationScenario(spec NetworkSpec, controller ValidatorChaosController, target NodeID, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	if target == "" && len(ids) > 0 {
		target = ids[len(ids)-1]
	}
	baseline := &ValidatorPowerBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	isolationAssertions := []Assertion{
		ValidatorPowerRestored(baseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "endpoint-lie-consensus-isolation",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 || target == "" || controller.EndpointMutator == nil {
		return scenario
	}

	badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", target)
	scenario.Steps = append(scenario.Steps,
		ActionStep("capture validator power baseline", CaptureValidatorPowerBaseline(baseline)),
		Step{
			Name:       "advertise bad endpoint; consensus outcome is unchanged",
			Actions:    []Action{AdvertiseEndpointWith(controller.EndpointMutator, target, badEndpoint)},
			Assertions: isolationAssertions,
			Timeout:    stepTimeout,
		},
		Step{
			Name:       "repair endpoint; consensus outcome remains unchanged",
			Actions:    []Action{AdvertiseEndpointWith(controller.EndpointMutator, target, "")},
			Assertions: isolationAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func EndpointRepairIdempotencyScenario(spec NetworkSpec, controller ValidatorChaosController, target NodeID, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	if target == "" && len(ids) > 0 {
		target = ids[len(ids)-1]
	}
	powerBaseline := &ValidatorPowerBaseline{}
	reachabilityBaseline := &ReachabilityBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	consensusAssertions := []Assertion{
		ValidatorPowerRestored(powerBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	restorationAssertions := []Assertion{
		ValidatorPowerRestored(powerBaseline, within, pollInterval),
		ReachabilityRestored(reachabilityBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "endpoint-repair-idempotency",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 || target == "" || controller.EndpointMutator == nil {
		return scenario
	}

	badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", target)
	scenario.Steps = append(scenario.Steps,
		ActionStep("capture validator power baseline", CaptureValidatorPowerBaseline(powerBaseline)),
		ActionStep("capture reachability baseline", CaptureReachabilityBaseline(reachabilityBaseline)),
		Step{
			Name:       "repair already-honest endpoint; consensus outcome is unchanged",
			Actions:    []Action{AdvertiseEndpointWith(controller.EndpointMutator, target, "")},
			Assertions: restorationAssertions,
			Timeout:    stepTimeout,
		},
		Step{
			Name:       "advertise bad endpoint; consensus outcome is unchanged",
			Actions:    []Action{AdvertiseEndpointWith(controller.EndpointMutator, target, badEndpoint)},
			Assertions: consensusAssertions,
			Timeout:    stepTimeout,
		},
		Step{
			Name:       "repair bad endpoint; endpoint outcome is restored",
			Actions:    []Action{AdvertiseEndpointWith(controller.EndpointMutator, target, "")},
			Assertions: restorationAssertions,
			Timeout:    stepTimeout,
		},
		Step{
			Name:       "duplicate endpoint repair; endpoint outcome remains restored",
			Actions:    []Action{AdvertiseEndpointWith(controller.EndpointMutator, target, "")},
			Assertions: restorationAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func EndpointRegisterRoundTripScenario(spec NetworkSpec, controller ValidatorChaosController, target NodeID, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	if target == "" && len(ids) > 0 {
		target = ids[len(ids)-1]
	}
	powerBaseline := &ValidatorPowerBaseline{}
	reachabilityBaseline := &ReachabilityBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	restoreAssertions := []Assertion{
		ValidatorPowerRestored(powerBaseline, within, pollInterval),
		ReachabilityRestored(reachabilityBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "endpoint-register-round-trip",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 || target == "" || controller.EndpointMutator == nil || controller.Registrar == nil {
		return scenario
	}

	badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", target)
	scenario.Steps = append(scenario.Steps,
		ActionStep("capture initial validator power baseline", CaptureValidatorPowerBaseline(powerBaseline)),
		ActionStep("capture initial reachability baseline", CaptureReachabilityBaseline(reachabilityBaseline)),
		outcomeActionStep("advertise bad endpoint; chain keeps validator outcome", stepTimeout, []Action{AdvertiseEndpointWith(controller.EndpointMutator, target, badEndpoint)}, outcomeAssertions),
		outcomeActionStep("deregister validator with bad endpoint; chain follows updated set", stepTimeout, []Action{DeregisterNodeWith(controller.Registrar, target)}, outcomeAssertions),
		Step{
			Name:       "register validator; chain restores original validator and endpoint outcome",
			Actions:    []Action{RegisterNodeWith(controller.Registrar, target)},
			Assertions: restoreAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func CohortEndpointConsensusIsolationScenario(spec NetworkSpec, controller ValidatorChaosController, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	cohort := quorumLossCohort(ids)
	baseline := &ValidatorPowerBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	isolationAssertions := []Assertion{
		ValidatorPowerRestored(baseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "cohort-endpoint-consensus-isolation",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) <= 1 || len(cohort) == 0 || controller.EndpointMutator == nil {
		return scenario
	}

	scenario.Steps = append(scenario.Steps,
		ActionStep("capture validator power baseline", CaptureValidatorPowerBaseline(baseline)),
		Step{
			Name:       fmt.Sprintf("advertise bad endpoints for %d validators; consensus outcome is unchanged", len(cohort)),
			Actions:    endpointLieActions(controller.EndpointMutator, cohort),
			Assertions: isolationAssertions,
			Timeout:    stepTimeout,
		},
		Step{
			Name:       fmt.Sprintf("repair bad endpoints for %d validators; consensus outcome remains unchanged", len(cohort)),
			Actions:    endpointRepairActions(controller.EndpointMutator, cohort),
			Assertions: isolationAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func InactiveEndpointIsolationScenario(spec NetworkSpec, controller ValidatorChaosController, target NodeID, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	if target == "" && len(ids) > 0 {
		target = ids[len(ids)-1]
	}
	postJailBaseline := &ValidatorPowerBaseline{}
	postDeregisterBaseline := &ValidatorPowerBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	postJailAssertions := []Assertion{
		ValidatorPowerRestored(postJailBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	postDeregisterAssertions := []Assertion{
		ValidatorPowerRestored(postDeregisterBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "inactive-endpoint-isolation",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 || target == "" || controller.EndpointMutator == nil || controller.Jailer == nil || controller.Registrar == nil {
		return scenario
	}

	badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", target)
	scenario.Steps = append(scenario.Steps,
		outcomeActionStep("jail validator; chain follows updated set", stepTimeout, []Action{JailNodeWith(controller.Jailer, target)}, outcomeAssertions),
		ActionStep("capture post-jail validator power baseline", CaptureValidatorPowerBaseline(postJailBaseline)),
		Step{
			Name:       "advertise bad endpoint for jailed validator; chain keeps same validator outcome",
			Actions:    []Action{AdvertiseEndpointWith(controller.EndpointMutator, target, badEndpoint)},
			Assertions: postJailAssertions,
			Timeout:    stepTimeout,
		},
		Step{
			Name:       "repair endpoint for jailed validator; chain keeps same validator outcome",
			Actions:    []Action{AdvertiseEndpointWith(controller.EndpointMutator, target, "")},
			Assertions: postJailAssertions,
			Timeout:    stepTimeout,
		},
		outcomeActionStep("deregister jailed validator; chain keeps updated set", stepTimeout, []Action{DeregisterNodeWith(controller.Registrar, target)}, outcomeAssertions),
		ActionStep("capture post-deregister validator power baseline", CaptureValidatorPowerBaseline(postDeregisterBaseline)),
		Step{
			Name:       "advertise bad endpoint for absent validator; chain keeps same validator outcome",
			Actions:    []Action{AdvertiseEndpointWith(controller.EndpointMutator, target, badEndpoint)},
			Assertions: postDeregisterAssertions,
			Timeout:    stepTimeout,
		},
		Step{
			Name:       "repair endpoint for absent validator; chain keeps same validator outcome",
			Actions:    []Action{AdvertiseEndpointWith(controller.EndpointMutator, target, "")},
			Assertions: postDeregisterAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func JailedEndpointRepairRoundTripScenario(spec NetworkSpec, controller ValidatorChaosController, target NodeID, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	if target == "" && len(ids) > 0 {
		target = ids[len(ids)-1]
	}
	initialPowerBaseline := &ValidatorPowerBaseline{}
	initialReachabilityBaseline := &ReachabilityBaseline{}
	postJailPowerBaseline := &ValidatorPowerBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	postJailAssertions := []Assertion{
		ValidatorPowerRestored(postJailPowerBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	restoreAssertions := []Assertion{
		ValidatorPowerRestored(initialPowerBaseline, within, pollInterval),
		ReachabilityRestored(initialReachabilityBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "jailed-endpoint-repair-round-trip",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 || target == "" || controller.EndpointMutator == nil || controller.Jailer == nil {
		return scenario
	}

	badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", target)
	scenario.Steps = append(scenario.Steps,
		ActionStep("capture initial validator power baseline", CaptureValidatorPowerBaseline(initialPowerBaseline)),
		ActionStep("capture initial reachability baseline", CaptureReachabilityBaseline(initialReachabilityBaseline)),
		outcomeActionStep("jail validator; chain follows updated set", stepTimeout, []Action{JailNodeWith(controller.Jailer, target)}, outcomeAssertions),
		ActionStep("capture post-jail validator power baseline", CaptureValidatorPowerBaseline(postJailPowerBaseline)),
		Step{
			Name:       "advertise bad endpoint for jailed validator; chain keeps updated validator outcome",
			Actions:    []Action{AdvertiseEndpointWith(controller.EndpointMutator, target, badEndpoint)},
			Assertions: postJailAssertions,
			Timeout:    stepTimeout,
		},
		Step{
			Name:       "repair endpoint for jailed validator; chain keeps updated validator outcome",
			Actions:    []Action{AdvertiseEndpointWith(controller.EndpointMutator, target, "")},
			Assertions: postJailAssertions,
			Timeout:    stepTimeout,
		},
		Step{
			Name:       "unjail validator; chain restores original validator and endpoint outcome",
			Actions:    []Action{UnjailNodeWith(controller.Jailer, target)},
			Assertions: restoreAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func StopStartRoundTripScenario(spec NetworkSpec, target NodeID, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	if target == "" && len(ids) > 0 {
		target = ids[len(ids)-1]
	}
	initialPowerBaseline := &ValidatorPowerBaseline{}
	initialReachabilityBaseline := &ReachabilityBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	restoreAssertions := []Assertion{
		ValidatorPowerRestored(initialPowerBaseline, within, pollInterval),
		ReachabilityRestored(initialReachabilityBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "stop-start-round-trip",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 || target == "" {
		return scenario
	}

	scenario.Steps = append(scenario.Steps,
		ActionStep("capture initial validator power baseline", CaptureValidatorPowerBaseline(initialPowerBaseline)),
		ActionStep("capture initial reachability baseline", CaptureReachabilityBaseline(initialReachabilityBaseline)),
		outcomeActionStep("stop validator; chain follows live validator power", stepTimeout, []Action{StopNode(target)}, outcomeAssertions),
		Step{
			Name:       "start validator; chain restores original live validator and endpoint outcome",
			Actions:    []Action{StartNode(target)},
			Assertions: restoreAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func InactiveStartIsolationScenario(spec NetworkSpec, controller ValidatorChaosController, target NodeID, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	if target == "" && len(ids) > 0 {
		target = ids[len(ids)-1]
	}
	postJailBaseline := &ValidatorPowerBaseline{}
	postDeregisterBaseline := &ValidatorPowerBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	postJailAssertions := []Assertion{
		ValidatorPowerRestored(postJailBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	postDeregisterAssertions := []Assertion{
		ValidatorPowerRestored(postDeregisterBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "inactive-start-isolation",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 || target == "" || controller.Jailer == nil || controller.Registrar == nil {
		return scenario
	}

	scenario.Steps = append(scenario.Steps,
		outcomeActionStep("jail validator; chain follows updated set", stepTimeout, []Action{JailNodeWith(controller.Jailer, target)}, outcomeAssertions),
		ActionStep("capture post-jail validator power baseline", CaptureValidatorPowerBaseline(postJailBaseline)),
		Step{
			Name:       "start jailed validator; chain keeps same validator outcome",
			Actions:    []Action{StartNode(target)},
			Assertions: postJailAssertions,
			Timeout:    stepTimeout,
		},
		outcomeActionStep("deregister jailed validator; chain keeps updated set", stepTimeout, []Action{DeregisterNodeWith(controller.Registrar, target)}, outcomeAssertions),
		ActionStep("capture post-deregister validator power baseline", CaptureValidatorPowerBaseline(postDeregisterBaseline)),
		Step{
			Name:       "start absent validator; chain keeps same validator outcome",
			Actions:    []Action{StartNode(target)},
			Assertions: postDeregisterAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func NonJailedUnjailIsolationScenario(spec NetworkSpec, controller ValidatorChaosController, target NodeID, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	if target == "" && len(ids) > 0 {
		target = ids[len(ids)-1]
	}
	initialBaseline := &ValidatorPowerBaseline{}
	postDeregisterBaseline := &ValidatorPowerBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	activeNoopAssertions := []Assertion{
		ValidatorPowerRestored(initialBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	absentNoopAssertions := []Assertion{
		ValidatorPowerRestored(postDeregisterBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "non-jailed-unjail-isolation",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 || target == "" || controller.Jailer == nil || controller.Registrar == nil {
		return scenario
	}

	scenario.Steps = append(scenario.Steps,
		ActionStep("capture initial validator power baseline", CaptureValidatorPowerBaseline(initialBaseline)),
		Step{
			Name:       "unjail active validator; chain keeps same validator outcome",
			Actions:    []Action{UnjailNodeWith(controller.Jailer, target)},
			Assertions: activeNoopAssertions,
			Timeout:    stepTimeout,
		},
		outcomeActionStep("deregister validator; chain follows updated set", stepTimeout, []Action{DeregisterNodeWith(controller.Registrar, target)}, outcomeAssertions),
		ActionStep("capture post-deregister validator power baseline", CaptureValidatorPowerBaseline(postDeregisterBaseline)),
		Step{
			Name:       "unjail absent validator; chain keeps same validator outcome",
			Actions:    []Action{UnjailNodeWith(controller.Jailer, target)},
			Assertions: absentNoopAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func RegisterRoundTripScenario(spec NetworkSpec, controller ValidatorChaosController, target NodeID, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	if target == "" && len(ids) > 0 {
		target = ids[len(ids)-1]
	}
	initialPowerBaseline := &ValidatorPowerBaseline{}
	initialReachabilityBaseline := &ReachabilityBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	restoreAssertions := []Assertion{
		ValidatorPowerRestored(initialPowerBaseline, within, pollInterval),
		ReachabilityRestored(initialReachabilityBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "register-round-trip",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 || target == "" || controller.Registrar == nil {
		return scenario
	}

	scenario.Steps = append(scenario.Steps,
		ActionStep("capture initial validator power baseline", CaptureValidatorPowerBaseline(initialPowerBaseline)),
		ActionStep("capture initial reachability baseline", CaptureReachabilityBaseline(initialReachabilityBaseline)),
		outcomeActionStep("deregister validator; chain follows updated set", stepTimeout, []Action{DeregisterNodeWith(controller.Registrar, target)}, outcomeAssertions),
		Step{
			Name:       "register validator; chain restores original validator and endpoint outcome",
			Actions:    []Action{RegisterNodeWith(controller.Registrar, target)},
			Assertions: restoreAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func JailedRegisterRoundTripScenario(spec NetworkSpec, controller ValidatorChaosController, target NodeID, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	if target == "" && len(ids) > 0 {
		target = ids[len(ids)-1]
	}
	initialPowerBaseline := &ValidatorPowerBaseline{}
	initialReachabilityBaseline := &ReachabilityBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	restoreAssertions := []Assertion{
		ValidatorPowerRestored(initialPowerBaseline, within, pollInterval),
		ReachabilityRestored(initialReachabilityBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "jailed-register-round-trip",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 || target == "" || controller.Jailer == nil || controller.Registrar == nil {
		return scenario
	}

	scenario.Steps = append(scenario.Steps,
		ActionStep("capture initial validator power baseline", CaptureValidatorPowerBaseline(initialPowerBaseline)),
		ActionStep("capture initial reachability baseline", CaptureReachabilityBaseline(initialReachabilityBaseline)),
		outcomeActionStep("jail validator; chain follows updated set", stepTimeout, []Action{JailNodeWith(controller.Jailer, target)}, outcomeAssertions),
		Step{
			Name:       "register jailed validator; chain restores original validator and endpoint outcome",
			Actions:    []Action{RegisterNodeWith(controller.Registrar, target)},
			Assertions: restoreAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func RegisterIdempotencyScenario(spec NetworkSpec, controller ValidatorChaosController, target NodeID, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	if target == "" && len(ids) > 0 {
		target = ids[len(ids)-1]
	}
	initialPowerBaseline := &ValidatorPowerBaseline{}
	initialReachabilityBaseline := &ReachabilityBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	idempotencyAssertions := []Assertion{
		ValidatorPowerRestored(initialPowerBaseline, within, pollInterval),
		ReachabilityRestored(initialReachabilityBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "register-idempotency",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 || target == "" || controller.Registrar == nil {
		return scenario
	}

	scenario.Steps = append(scenario.Steps,
		ActionStep("capture initial validator power baseline", CaptureValidatorPowerBaseline(initialPowerBaseline)),
		ActionStep("capture initial reachability baseline", CaptureReachabilityBaseline(initialReachabilityBaseline)),
		Step{
			Name:       "register active validator; chain keeps same validator and endpoint outcome",
			Actions:    []Action{RegisterNodeWith(controller.Registrar, target)},
			Assertions: idempotencyAssertions,
			Timeout:    stepTimeout,
		},
		outcomeActionStep("deregister validator; chain follows updated set", stepTimeout, []Action{DeregisterNodeWith(controller.Registrar, target)}, outcomeAssertions),
		Step{
			Name:       "register validator; chain restores original validator and endpoint outcome",
			Actions:    []Action{RegisterNodeWith(controller.Registrar, target)},
			Assertions: idempotencyAssertions,
			Timeout:    stepTimeout,
		},
		Step{
			Name:       "duplicate register; chain keeps restored validator and endpoint outcome",
			Actions:    []Action{RegisterNodeWith(controller.Registrar, target)},
			Assertions: idempotencyAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func UnjailRoundTripScenario(spec NetworkSpec, controller ValidatorChaosController, target NodeID, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	if target == "" && len(ids) > 0 {
		target = ids[len(ids)-1]
	}
	initialPowerBaseline := &ValidatorPowerBaseline{}
	initialReachabilityBaseline := &ReachabilityBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	restoreAssertions := []Assertion{
		ValidatorPowerRestored(initialPowerBaseline, within, pollInterval),
		ReachabilityRestored(initialReachabilityBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "unjail-round-trip",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 || target == "" || controller.Jailer == nil {
		return scenario
	}

	scenario.Steps = append(scenario.Steps,
		ActionStep("capture initial validator power baseline", CaptureValidatorPowerBaseline(initialPowerBaseline)),
		ActionStep("capture initial reachability baseline", CaptureReachabilityBaseline(initialReachabilityBaseline)),
		outcomeActionStep("jail validator; chain follows updated set", stepTimeout, []Action{JailNodeWith(controller.Jailer, target)}, outcomeAssertions),
		Step{
			Name:       "unjail validator; chain restores original validator and endpoint outcome",
			Actions:    []Action{UnjailNodeWith(controller.Jailer, target)},
			Assertions: restoreAssertions,
			Timeout:    stepTimeout,
		},
	)
	return scenario
}

func CohortLifecycleRoundTripScenario(spec NetworkSpec, controller ValidatorChaosController, within, pollInterval time.Duration) Scenario {
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

	ids := spec.NodeIDs()
	cohort := quorumLossCohort(ids)
	initialPowerBaseline := &ValidatorPowerBaseline{}
	initialReachabilityBaseline := &ReachabilityBaseline{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	restoreAssertions := []Assertion{
		ValidatorPowerRestored(initialPowerBaseline, within, pollInterval),
		ReachabilityRestored(initialReachabilityBaseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "cohort-lifecycle-round-trip",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(cohort) == 0 || (controller.Registrar == nil && controller.Jailer == nil) {
		return scenario
	}

	scenario.Steps = append(scenario.Steps,
		ActionStep("capture initial validator power baseline", CaptureValidatorPowerBaseline(initialPowerBaseline)),
		ActionStep("capture initial reachability baseline", CaptureReachabilityBaseline(initialReachabilityBaseline)),
	)
	if controller.Registrar != nil {
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep(fmt.Sprintf("deregister %d validators; chain follows updated set", len(cohort)), stepTimeout, deregisterActions(controller.Registrar, cohort), outcomeAssertions),
			Step{
				Name:       fmt.Sprintf("register %d validators; chain restores original validator and endpoint outcome", len(cohort)),
				Actions:    registerActions(controller.Registrar, cohort),
				Assertions: restoreAssertions,
				Timeout:    stepTimeout,
			},
		)
	}
	if controller.Jailer != nil {
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep(fmt.Sprintf("jail %d validators; chain follows updated set", len(cohort)), stepTimeout, jailActions(controller.Jailer, cohort), outcomeAssertions),
			Step{
				Name:       fmt.Sprintf("unjail %d validators; chain restores original validator and endpoint outcome", len(cohort)),
				Actions:    unjailActions(controller.Jailer, cohort),
				Assertions: restoreAssertions,
				Timeout:    stepTimeout,
			},
		)
	}
	return scenario
}

func MixedLifecycleQuorumRecoveryScenario(spec NetworkSpec, controller ValidatorChaosController, within, pollInterval time.Duration) Scenario {
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

	baseline := &ValidatorPowerBaseline{}
	state := &mixedLifecycleQuorumState{}
	outcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	stallAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	recoveryAssertions := []Assertion{
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	restoreAssertions := []Assertion{
		ValidatorPowerRestored(baseline, within, pollInterval),
		HeightAdvances(1, within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	scenario := Scenario{
		Name: "mixed-lifecycle-quorum-recovery",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(spec.NodeIDs()) < 4 || (controller.Registrar == nil && controller.Jailer == nil) {
		return scenario
	}

	scenario.Steps = append(scenario.Steps,
		ActionStep("capture initial validator power baseline", CaptureValidatorPowerBaseline(baseline)),
		ActionStep("plan mixed lifecycle quorum boundary", planMixedLifecycleQuorum(state)),
	)
	if controller.Registrar != nil {
		scenario.Steps = append(scenario.Steps,
			Step{
				Name:       "deregister planned validators; chain follows updated set",
				Actions:    []Action{deregisterMixedLifecycleRemoved(state, controller.Registrar)},
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
			Step{
				Name:       "stop planned remaining validators; chain stalls without quorum",
				Actions:    []Action{stopMixedLifecycleStopped(state)},
				Assertions: stallAssertions,
				Timeout:    stepTimeout,
			},
			Step{
				Name:       "register removed validators; chain recovers before stopped validators restart",
				Actions:    []Action{registerMixedLifecycleRemoved(state, controller.Registrar)},
				Assertions: recoveryAssertions,
				Timeout:    stepTimeout,
			},
			Step{
				Name:       "restart stopped validators; original validator outcome is restored",
				Actions:    []Action{startMixedLifecycleStopped(state)},
				Assertions: restoreAssertions,
				Timeout:    stepTimeout,
			},
		)
	}
	if controller.Jailer != nil {
		scenario.Steps = append(scenario.Steps,
			Step{
				Name:       "jail planned validators; chain follows updated set",
				Actions:    []Action{jailMixedLifecycleRemoved(state, controller.Jailer)},
				Assertions: outcomeAssertions,
				Timeout:    stepTimeout,
			},
			Step{
				Name:       "stop planned remaining validators; chain stalls without quorum",
				Actions:    []Action{stopMixedLifecycleStopped(state)},
				Assertions: stallAssertions,
				Timeout:    stepTimeout,
			},
			Step{
				Name:       "unjail removed validators; chain recovers before stopped validators restart",
				Actions:    []Action{unjailMixedLifecycleRemoved(state, controller.Jailer)},
				Assertions: recoveryAssertions,
				Timeout:    stepTimeout,
			},
			Step{
				Name:       "restart stopped validators; original validator outcome is restored",
				Actions:    []Action{startMixedLifecycleStopped(state)},
				Assertions: restoreAssertions,
				Timeout:    stepTimeout,
			},
		)
	}
	return scenario
}

func CompoundOutcomeEdgeCaseScenario(spec NetworkSpec, controller ValidatorChaosController, within, pollInterval time.Duration) Scenario {
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
	quorumOutcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	ids := spec.NodeIDs()
	scenario := Scenario{
		Name: "compound-outcome-edge-cases",
		Steps: []Step{
			{
				Name:       "initial height advances",
				Assertions: quorumOutcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if len(ids) == 0 {
		return scenario
	}

	if controller.EndpointMutator != nil {
		lieIDs := quorumLossCohort(ids)
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep(fmt.Sprintf("advertise bad endpoints for %d validators; chain still progresses", len(lieIDs)), stepTimeout, endpointLieActions(controller.EndpointMutator, lieIDs), quorumOutcomeAssertions),
			outcomeActionStep(fmt.Sprintf("repair bad endpoints for %d validators", len(lieIDs)), stepTimeout, endpointRepairActions(controller.EndpointMutator, lieIDs), quorumOutcomeAssertions),
		)
	}

	if preserve := quorumPreservingCohort(ids); len(preserve) > 0 {
		stop, start := stopStartActions(preserve)
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep(fmt.Sprintf("stop %d validators leaving minimum quorum; chain still progresses", len(preserve)), stepTimeout, stop, quorumOutcomeAssertions),
		)

		liveIDs := nodeSetDifference(ids, preserve)
		if controller.EndpointMutator != nil {
			lieIDs := quorumLossCohort(liveIDs)
			scenario.Steps = append(scenario.Steps,
				outcomeActionStep(fmt.Sprintf("advertise bad endpoints for %d live validators at quorum boundary; chain still progresses", len(lieIDs)), stepTimeout, endpointLieActions(controller.EndpointMutator, lieIDs), quorumOutcomeAssertions),
			)
		}

		breaker := liveIDs[0]
		scenario.Steps = append(scenario.Steps,
			Step{
				Name:       "stop one more validator across quorum boundary; chain stalls",
				Actions:    []Action{StopNode(breaker)},
				Assertions: []Assertion{HeightFollowsValidatorQuorum(within, pollInterval)},
				Timeout:    stepTimeout,
			},
			outcomeActionStep("restart boundary validator; chain recovers", stepTimeout, []Action{StartNode(breaker)}, quorumOutcomeAssertions),
		)
		if controller.EndpointMutator != nil {
			lieIDs := quorumLossCohort(liveIDs)
			scenario.Steps = append(scenario.Steps,
				outcomeActionStep(fmt.Sprintf("repair bad endpoints for %d boundary validators", len(lieIDs)), stepTimeout, endpointRepairActions(controller.EndpointMutator, lieIDs), quorumOutcomeAssertions),
			)
		}
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep(fmt.Sprintf("restart %d stopped validators; chain remains live", len(preserve)), stepTimeout, start, quorumOutcomeAssertions),
		)
	} else {
		first := ids[0]
		scenario.Steps = append(scenario.Steps,
			Step{
				Name:       "stop sole validator; chain stalls",
				Actions:    []Action{StopNode(first)},
				Assertions: []Assertion{HeightFollowsValidatorQuorum(within, pollInterval)},
				Timeout:    stepTimeout,
			},
			outcomeActionStep("restart sole validator; chain recovers", stepTimeout, []Action{StartNode(first)}, quorumOutcomeAssertions),
		)
	}

	if controller.Registrar != nil {
		cohort := quorumLossCohort(ids)
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep(fmt.Sprintf("deregister %d validators; chain follows updated validator set", len(cohort)), stepTimeout, deregisterActions(controller.Registrar, cohort), quorumOutcomeAssertions),
			outcomeActionStep(fmt.Sprintf("duplicate deregister %d validators; chain follows updated validator set", len(cohort)), stepTimeout, deregisterActions(controller.Registrar, cohort), quorumOutcomeAssertions),
			outcomeActionStep(fmt.Sprintf("register %d validators; chain remains live", len(cohort)), stepTimeout, registerActions(controller.Registrar, cohort), quorumOutcomeAssertions),
		)
	}

	if controller.Jailer != nil {
		cohort := quorumLossCohort(ids)
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep(fmt.Sprintf("jail %d validators; chain follows updated validator set", len(cohort)), stepTimeout, jailActions(controller.Jailer, cohort), quorumOutcomeAssertions),
		)
		if controller.Registrar != nil {
			scenario.Steps = append(scenario.Steps,
				outcomeActionStep(fmt.Sprintf("deregister %d jailed validators; chain follows updated validator set", len(cohort)), stepTimeout, deregisterActions(controller.Registrar, cohort), quorumOutcomeAssertions),
				outcomeActionStep(fmt.Sprintf("register %d jailed-then-deregistered validators; chain remains live", len(cohort)), stepTimeout, registerActions(controller.Registrar, cohort), quorumOutcomeAssertions),
			)
		} else {
			scenario.Steps = append(scenario.Steps,
				outcomeActionStep(fmt.Sprintf("unjail %d validators; chain remains live", len(cohort)), stepTimeout, unjailActions(controller.Jailer, cohort), quorumOutcomeAssertions),
			)
		}
	}

	return scenario
}

func PowerSkewOutcomeScenario(spec NetworkSpec, controller ValidatorChaosController, highPowerID NodeID, lowPowerIDs []NodeID, within, pollInterval time.Duration) Scenario {
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
	quorumOutcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}

	ids := spec.NodeIDs()
	lowPowerIDs = nodeSetDifference(validNodeIDs(spec, lowPowerIDs), []NodeID{highPowerID})
	scenario := Scenario{
		Name: "power-skew-outcome-edge-cases",
		Steps: []Step{
			{
				Name:       "initial height advances",
				Assertions: quorumOutcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
	if _, ok := spec.Node(highPowerID); !ok || len(lowPowerIDs) == 0 {
		return scenario
	}

	stopLow, startLow := stopStartActions(lowPowerIDs)
	scenario.Steps = append(scenario.Steps,
		outcomeActionStep(fmt.Sprintf("stop %d low-power validators; chain follows validator power", len(lowPowerIDs)), stepTimeout, stopLow, quorumOutcomeAssertions),
	)

	if controller.EndpointMutator != nil {
		badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", highPowerID)
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep("advertise bad endpoint for high-power validator; chain follows validator power", stepTimeout, []Action{AdvertiseEndpointWith(controller.EndpointMutator, highPowerID, badEndpoint)}, quorumOutcomeAssertions),
		)
	}

	scenario.Steps = append(scenario.Steps,
		Step{
			Name:       "stop high-power validator while low-power validators are down; chain stalls",
			Actions:    []Action{StopNode(highPowerID)},
			Assertions: []Assertion{HeightFollowsValidatorQuorum(within, pollInterval)},
			Timeout:    stepTimeout,
		},
		outcomeActionStep("restart high-power validator; chain recovers by voting power", stepTimeout, []Action{StartNode(highPowerID)}, quorumOutcomeAssertions),
	)

	if controller.EndpointMutator != nil {
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep("repair high-power validator endpoint", stepTimeout, []Action{AdvertiseEndpointWith(controller.EndpointMutator, highPowerID, "")}, quorumOutcomeAssertions),
		)
	}
	scenario.Steps = append(scenario.Steps,
		outcomeActionStep(fmt.Sprintf("restart %d low-power validators; chain remains live", len(lowPowerIDs)), stepTimeout, startLow, quorumOutcomeAssertions),
		Step{
			Name:       "stop high-power validator alone; chain stalls despite most nodes being live",
			Actions:    []Action{StopNode(highPowerID)},
			Assertions: []Assertion{HeightFollowsValidatorQuorum(within, pollInterval)},
			Timeout:    stepTimeout,
		},
		outcomeActionStep("restart high-power validator; chain recovers", stepTimeout, []Action{StartNode(highPowerID)}, quorumOutcomeAssertions),
	)

	if controller.Jailer != nil {
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep("jail high-power validator; chain follows updated validator set", stepTimeout, []Action{JailNodeWith(controller.Jailer, highPowerID)}, quorumOutcomeAssertions),
		)
		if controller.Registrar != nil {
			scenario.Steps = append(scenario.Steps,
				outcomeActionStep("deregister jailed high-power validator; chain follows updated validator set", stepTimeout, []Action{DeregisterNodeWith(controller.Registrar, highPowerID)}, quorumOutcomeAssertions),
				outcomeActionStep("register high-power validator; chain follows restored validator set", stepTimeout, []Action{RegisterNodeWith(controller.Registrar, highPowerID)}, quorumOutcomeAssertions),
			)
		} else {
			scenario.Steps = append(scenario.Steps,
				outcomeActionStep("unjail high-power validator; chain follows restored validator set", stepTimeout, []Action{UnjailNodeWith(controller.Jailer, highPowerID)}, quorumOutcomeAssertions),
			)
		}
	}

	if controller.Registrar != nil && len(ids) > len(lowPowerIDs)+1 {
		otherIDs := nodeSetDifference(ids, append([]NodeID{highPowerID}, lowPowerIDs...))
		scenario.Steps = append(scenario.Steps,
			outcomeActionStep(fmt.Sprintf("deregister %d remaining low-power validators; chain follows updated validator set", len(otherIDs)), stepTimeout, deregisterActions(controller.Registrar, otherIDs), quorumOutcomeAssertions),
			outcomeActionStep(fmt.Sprintf("register %d remaining low-power validators; chain follows restored validator set", len(otherIDs)), stepTimeout, registerActions(controller.Registrar, otherIDs), quorumOutcomeAssertions),
		)
	}

	return scenario
}

func PowerBoundaryOutcomeScenario(within, pollInterval time.Duration) Scenario {
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
	quorumOutcomeAssertions := []Assertion{
		HeightFollowsValidatorQuorum(within, pollInterval),
		LiveValidatorHeightsConverge(0, within, pollInterval),
		NoLiveValidatorFork(),
		NoHeightRegression(regressionWindow, pollInterval),
	}
	state := &powerBoundaryState{}

	return Scenario{
		Name: "power-boundary-outcome-edge-cases",
		Steps: []Step{
			{
				Name:       "initial height advances",
				Assertions: quorumOutcomeAssertions,
				Timeout:    stepTimeout,
			},
			{
				Name:       "stop largest observed power partition that preserves quorum",
				Actions:    []Action{planAndStopPowerBoundary(state)},
				Assertions: quorumOutcomeAssertions,
				Timeout:    stepTimeout,
			},
			{
				Name:       "stop next validator across observed power quorum boundary",
				Actions:    []Action{stopPowerBoundaryBreaker(state)},
				Assertions: []Assertion{HeightFollowsValidatorQuorum(within, pollInterval)},
				Timeout:    stepTimeout,
			},
			{
				Name:       "restart boundary validator; chain recovers",
				Actions:    []Action{restartPowerBoundaryBreaker(state)},
				Assertions: quorumOutcomeAssertions,
				Timeout:    stepTimeout,
			},
			{
				Name:       "restart power partition; chain remains live",
				Actions:    []Action{restartPowerBoundaryPartition(state)},
				Assertions: quorumOutcomeAssertions,
				Timeout:    stepTimeout,
			},
		},
	}
}

type powerBoundaryState struct {
	plan powerBoundaryPlan
}

type powerBoundaryPlan struct {
	preserve          []NodeID
	breaker           NodeID
	totalPower        int64
	livePowerBefore   int64
	livePowerAfter    int64
	livePowerBreakage int64
}

type validatorPowerSample struct {
	id    NodeID
	power int64
}

func planAndStopPowerBoundary(state *powerBoundaryState) Action {
	return ActionFunc{
		Label: "plan and stop power-boundary partition",
		Fn: func(ctx context.Context, run *RunContext) error {
			snapshot, err := run.Network.Snapshot(ctx)
			if err != nil {
				return err
			}
			plan, err := quorumBoundaryPlan(snapshot)
			if err != nil {
				return err
			}
			state.plan = plan
			return Parallel("stop power-boundary partition", stopActions(plan.preserve)...).Run(ctx, run)
		},
	}
}

func stopPowerBoundaryBreaker(state *powerBoundaryState) Action {
	return ActionFunc{
		Label: "stop power-boundary breaker",
		Fn: func(ctx context.Context, run *RunContext) error {
			if state.plan.breaker == "" {
				return fmt.Errorf("%w: power boundary breaker was not planned", ErrInvalidScenario)
			}
			return StopNode(state.plan.breaker).Run(ctx, run)
		},
	}
}

func restartPowerBoundaryBreaker(state *powerBoundaryState) Action {
	return ActionFunc{
		Label: "restart power-boundary breaker",
		Fn: func(ctx context.Context, run *RunContext) error {
			if state.plan.breaker == "" {
				return fmt.Errorf("%w: power boundary breaker was not planned", ErrInvalidScenario)
			}
			return StartNode(state.plan.breaker).Run(ctx, run)
		},
	}
}

func restartPowerBoundaryPartition(state *powerBoundaryState) Action {
	return ActionFunc{
		Label: "restart power-boundary partition",
		Fn: func(ctx context.Context, run *RunContext) error {
			return Parallel("restart power-boundary partition", startActions(state.plan.preserve)...).Run(ctx, run)
		},
	}
}

func quorumBoundaryPlan(snapshot Snapshot) (powerBoundaryPlan, error) {
	totalPower, livePower := snapshot.ValidatorPower()
	if totalPower <= 0 {
		return powerBoundaryPlan{}, fmt.Errorf("%w: snapshot has no validator power: %s", ErrInvalidScenario, snapshot.Summary())
	}
	if livePower*3 <= totalPower*2 {
		return powerBoundaryPlan{}, fmt.Errorf("%w: snapshot already lacks validator quorum power=%d/%d: %s", ErrInvalidScenario, livePower, totalPower, snapshot.Summary())
	}

	live := liveValidatorPowers(snapshot)
	if len(live) == 0 {
		return powerBoundaryPlan{}, fmt.Errorf("%w: snapshot has no live validators: %s", ErrInvalidScenario, snapshot.Summary())
	}

	plan := powerBoundaryPlan{
		totalPower:      totalPower,
		livePowerBefore: livePower,
		livePowerAfter:  livePower,
	}
	for _, validator := range live {
		nextLivePower := plan.livePowerAfter - validator.power
		if nextLivePower*3 > totalPower*2 {
			plan.preserve = append(plan.preserve, validator.id)
			plan.livePowerAfter = nextLivePower
		}
	}

	preserved := make(map[NodeID]struct{}, len(plan.preserve))
	for _, id := range plan.preserve {
		preserved[id] = struct{}{}
	}
	for _, validator := range live {
		if _, ok := preserved[validator.id]; ok {
			continue
		}
		nextLivePower := plan.livePowerAfter - validator.power
		if nextLivePower*3 <= totalPower*2 {
			plan.breaker = validator.id
			plan.livePowerBreakage = nextLivePower
			return plan, nil
		}
	}

	return powerBoundaryPlan{}, fmt.Errorf("%w: could not find validator that crosses power quorum boundary from power=%d/%d: %s", ErrInvalidScenario, plan.livePowerAfter, totalPower, snapshot.Summary())
}

func liveValidatorPowers(snapshot Snapshot) []validatorPowerSample {
	out := make([]validatorPowerSample, 0, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		if !node.Live || node.ValidatorPower <= 0 {
			continue
		}
		out = append(out, validatorPowerSample{id: node.ID, power: node.ValidatorPower})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].power == out[j].power {
			return out[i].id < out[j].id
		}
		return out[i].power < out[j].power
	})
	return out
}

type mixedLifecycleQuorumState struct {
	plan mixedLifecycleQuorumPlan
}

type mixedLifecycleQuorumPlan struct {
	remove       []NodeID
	stop         []NodeID
	totalPower   int64
	removedPower int64
	stoppedPower int64
}

func planMixedLifecycleQuorum(state *mixedLifecycleQuorumState) Action {
	return ActionFunc{
		Label: "plan mixed lifecycle quorum boundary",
		Fn: func(ctx context.Context, run *RunContext) error {
			snapshot, err := run.Network.Snapshot(ctx)
			if err != nil {
				return err
			}
			plan, err := mixedLifecycleQuorumPlanFromSnapshot(snapshot)
			if err != nil {
				return err
			}
			state.plan = plan
			run.record(
				"mixed_lifecycle_quorum_plan",
				fmt.Sprintf("remove=%d stop=%d", len(plan.remove), len(plan.stop)),
				fmt.Sprintf("total_power=%d removed_power=%d stopped_power=%d", plan.totalPower, plan.removedPower, plan.stoppedPower),
			)
			return nil
		},
	}
}

func mixedLifecycleQuorumPlanFromSnapshot(snapshot Snapshot) (mixedLifecycleQuorumPlan, error) {
	totalPower, livePower := snapshot.ValidatorPower()
	if totalPower <= 0 {
		return mixedLifecycleQuorumPlan{}, fmt.Errorf("%w: snapshot has no validator power: %s", ErrInvalidScenario, snapshot.Summary())
	}
	if livePower*3 <= totalPower*2 {
		return mixedLifecycleQuorumPlan{}, fmt.Errorf("%w: snapshot already lacks validator quorum power=%d/%d: %s", ErrInvalidScenario, livePower, totalPower, snapshot.Summary())
	}

	live := liveValidatorPowers(snapshot)
	if len(live) < 4 {
		return mixedLifecycleQuorumPlan{}, fmt.Errorf("%w: mixed lifecycle quorum recovery requires at least 4 live validators: %s", ErrInvalidScenario, snapshot.Summary())
	}

	for removeCount := 1; removeCount < len(live); removeCount++ {
		removed := live[:removeCount]
		remaining := live[removeCount:]
		removedPower := validatorSamplePower(removed)
		totalAfterRemoval := totalPower - removedPower
		liveAfterRemoval := livePower - removedPower
		if totalAfterRemoval <= 0 || liveAfterRemoval*3 <= totalAfterRemoval*2 {
			continue
		}

		var stopped []validatorPowerSample
		var stoppedPower int64
		for _, validator := range remaining {
			stopped = append(stopped, validator)
			stoppedPower += validator.power
			liveAfterStop := liveAfterRemoval - stoppedPower
			if liveAfterStop*3 > totalAfterRemoval*2 {
				continue
			}
			liveAfterRecovery := livePower - stoppedPower
			if liveAfterRecovery*3 <= totalPower*2 {
				continue
			}
			return mixedLifecycleQuorumPlan{
				remove:       validatorSampleIDs(removed),
				stop:         validatorSampleIDs(stopped),
				totalPower:   totalPower,
				removedPower: removedPower,
				stoppedPower: stoppedPower,
			}, nil
		}
	}

	return mixedLifecycleQuorumPlan{}, fmt.Errorf("%w: could not find mixed lifecycle quorum boundary from power=%d/%d: %s", ErrInvalidScenario, livePower, totalPower, snapshot.Summary())
}

func validatorSamplePower(samples []validatorPowerSample) int64 {
	var power int64
	for _, sample := range samples {
		power += sample.power
	}
	return power
}

func validatorSampleIDs(samples []validatorPowerSample) []NodeID {
	ids := make([]NodeID, 0, len(samples))
	for _, sample := range samples {
		ids = append(ids, sample.id)
	}
	return ids
}

func deregisterMixedLifecycleRemoved(state *mixedLifecycleQuorumState, registrar Registrar) Action {
	return ActionFunc{
		Label: "deregister mixed lifecycle removed validators",
		Fn: func(ctx context.Context, run *RunContext) error {
			if err := ensureMixedLifecyclePlan(state); err != nil {
				return err
			}
			return Sequence("deregister planned validators", deregisterActions(registrar, state.plan.remove)...).Run(ctx, run)
		},
	}
}

func registerMixedLifecycleRemoved(state *mixedLifecycleQuorumState, registrar Registrar) Action {
	return ActionFunc{
		Label: "register mixed lifecycle removed validators",
		Fn: func(ctx context.Context, run *RunContext) error {
			if err := ensureMixedLifecyclePlan(state); err != nil {
				return err
			}
			return Sequence("register planned validators", registerActions(registrar, state.plan.remove)...).Run(ctx, run)
		},
	}
}

func jailMixedLifecycleRemoved(state *mixedLifecycleQuorumState, jailer Jailer) Action {
	return ActionFunc{
		Label: "jail mixed lifecycle removed validators",
		Fn: func(ctx context.Context, run *RunContext) error {
			if err := ensureMixedLifecyclePlan(state); err != nil {
				return err
			}
			return Sequence("jail planned validators", jailActions(jailer, state.plan.remove)...).Run(ctx, run)
		},
	}
}

func unjailMixedLifecycleRemoved(state *mixedLifecycleQuorumState, jailer Jailer) Action {
	return ActionFunc{
		Label: "unjail mixed lifecycle removed validators",
		Fn: func(ctx context.Context, run *RunContext) error {
			if err := ensureMixedLifecyclePlan(state); err != nil {
				return err
			}
			return Sequence("unjail planned validators", unjailActions(jailer, state.plan.remove)...).Run(ctx, run)
		},
	}
}

func stopMixedLifecycleStopped(state *mixedLifecycleQuorumState) Action {
	return ActionFunc{
		Label: "stop mixed lifecycle remaining validators",
		Fn: func(ctx context.Context, run *RunContext) error {
			if err := ensureMixedLifecyclePlan(state); err != nil {
				return err
			}
			return Parallel("stop planned remaining validators", stopActions(state.plan.stop)...).Run(ctx, run)
		},
	}
}

func startMixedLifecycleStopped(state *mixedLifecycleQuorumState) Action {
	return ActionFunc{
		Label: "start mixed lifecycle remaining validators",
		Fn: func(ctx context.Context, run *RunContext) error {
			if err := ensureMixedLifecyclePlan(state); err != nil {
				return err
			}
			return Parallel("start planned remaining validators", startActions(state.plan.stop)...).Run(ctx, run)
		},
	}
}

func ensureMixedLifecyclePlan(state *mixedLifecycleQuorumState) error {
	if state == nil || len(state.plan.remove) == 0 || len(state.plan.stop) == 0 {
		return fmt.Errorf("%w: mixed lifecycle quorum boundary was not planned", ErrInvalidScenario)
	}
	return nil
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

func validNodeIDs(spec NetworkSpec, ids []NodeID) []NodeID {
	out := make([]NodeID, 0, len(ids))
	seen := make(map[NodeID]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		if _, ok := spec.Node(id); !ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func nodeSetDifference(ids, excluded []NodeID) []NodeID {
	excludedSet := make(map[NodeID]struct{}, len(excluded))
	for _, id := range excluded {
		excludedSet[id] = struct{}{}
	}
	out := make([]NodeID, 0, len(ids))
	for _, id := range ids {
		if _, ok := excludedSet[id]; !ok {
			out = append(out, id)
		}
	}
	return out
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

func stopActions(ids []NodeID) []Action {
	actions := make([]Action, 0, len(ids))
	for _, id := range ids {
		actions = append(actions, StopNode(id))
	}
	return actions
}

func startActions(ids []NodeID) []Action {
	actions := make([]Action, 0, len(ids))
	for _, id := range ids {
		actions = append(actions, StartNode(id))
	}
	return actions
}

func quorumPreservingCohort(ids []NodeID) []NodeID {
	size := len(ids) - minimumQuorumNodes(len(ids))
	if size <= 0 {
		return nil
	}
	return append([]NodeID{}, ids[:size]...)
}

func endpointLieActions(controller EndpointMutator, ids []NodeID) []Action {
	actions := make([]Action, 0, len(ids))
	for _, id := range ids {
		badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", id)
		actions = append(actions, AdvertiseEndpointWith(controller, id, badEndpoint))
	}
	return actions
}

func endpointRepairActions(controller EndpointMutator, ids []NodeID) []Action {
	actions := make([]Action, 0, len(ids))
	for _, id := range ids {
		actions = append(actions, AdvertiseEndpointWith(controller, id, ""))
	}
	return actions
}

func registerActions(registrar Registrar, ids []NodeID) []Action {
	actions := make([]Action, 0, len(ids))
	for _, id := range ids {
		actions = append(actions, RegisterNodeWith(registrar, id))
	}
	return actions
}

func deregisterActions(registrar Registrar, ids []NodeID) []Action {
	actions := make([]Action, 0, len(ids))
	for _, id := range ids {
		actions = append(actions, DeregisterNodeWith(registrar, id))
	}
	return actions
}

func jailActions(jailer Jailer, ids []NodeID) []Action {
	actions := make([]Action, 0, len(ids))
	for _, id := range ids {
		actions = append(actions, JailNodeWith(jailer, id))
	}
	return actions
}

func unjailActions(jailer Jailer, ids []NodeID) []Action {
	actions := make([]Action, 0, len(ids))
	for _, id := range ids {
		actions = append(actions, UnjailNodeWith(jailer, id))
	}
	return actions
}

func minimumQuorumNodes(nodes int) int {
	if nodes <= 0 {
		return 0
	}
	return (nodes*2)/3 + 1
}
