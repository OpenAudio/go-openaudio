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
				Assertions: []Assertion{HeightFollowsValidatorQuorum(within, pollInterval), NoHeightRegression(regressionWindow, pollInterval)},
				Timeout:    stepTimeout,
			},
		},
	}
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
