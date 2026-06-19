package fuzz

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

type ValidatorChaosController struct {
	Registrar       Registrar
	EndpointMutator EndpointMutator
	Jailer          Jailer
}

type ValidatorChaosOptions struct {
	Seed                    int64
	Steps                   int
	StepTimeout             time.Duration
	LivenessEvery           int
	LivenessWithin          time.Duration
	PollInterval            time.Duration
	ActionNodeIDs           []NodeID
	StartNodes              bool
	IncludeProcessFaults    bool
	NoProcessFaultDelay     bool
	AssertAfterEachStep     bool
	AssertConvergence       bool
	IncludePersistentFaults bool
	RecoverAtEnd            bool
}

func ValidatorChaosScenario(spec NetworkSpec, controller ValidatorChaosController, opts ValidatorChaosOptions) Scenario {
	steps := clamp(opts.Steps, 1, defaultModelStepLimit)
	livenessEvery := opts.LivenessEvery
	assertAfterEachStep := opts.AssertAfterEachStep
	assertConvergence := opts.AssertConvergence
	livenessEvery, assertAfterEachStep, assertConvergence = generatedChaosAssertionOptions(livenessEvery, assertAfterEachStep, assertConvergence)
	livenessWithin := opts.LivenessWithin
	if livenessWithin <= 0 {
		livenessWithin = 30 * time.Second
	}
	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}

	rng := rand.New(rand.NewSource(opts.Seed))
	ids := spec.NodeIDs()
	actionIDs := opts.ActionNodeIDs
	if len(actionIDs) == 0 {
		actionIDs = ids
	}
	scenario := Scenario{
		Name: "validator-chaos",
		Seed: opts.Seed,
	}
	if opts.StartNodes {
		startActions := make([]Action, 0, len(ids))
		for _, id := range ids {
			startActions = append(startActions, StartNode(id))
		}
		scenario.Steps = append(scenario.Steps, ActionStep("start nodes", Parallel("start all nodes", startActions...)))
	}
	scenario.Steps = append(scenario.Steps,
		Step{
			Name:       "initial validator outcome",
			Assertions: append([]Assertion{AllReachable()}, ValidatorOutcomeAssertions(livenessWithin, pollInterval, assertConvergence)...),
		},
	)
	var recoveryPowerBaseline *ValidatorPowerBaseline
	var recoveryReachabilityBaseline *ReachabilityBaseline
	if opts.RecoverAtEnd {
		recoveryPowerBaseline = &ValidatorPowerBaseline{}
		recoveryReachabilityBaseline = &ReachabilityBaseline{}
		scenario.Steps = append(scenario.Steps,
			ActionStep("capture validator power baseline", CaptureValidatorPowerBaseline(recoveryPowerBaseline)),
			ActionStep("capture reachability baseline", CaptureReachabilityBaseline(recoveryReachabilityBaseline)),
		)
	}

	for i := 0; i < steps; i++ {
		step := Step{
			Name:    fmt.Sprintf("chaos %04d", i+1),
			Timeout: opts.StepTimeout,
		}
		if len(actionIDs) > 0 {
			id := actionIDs[rng.Intn(len(actionIDs))]
			step.Actions = append(step.Actions, randomChaosAction(rng, controller, opts, id, actionIDs, livenessWithin, pollInterval))
		}
		if shouldAssertGeneratedChaosStep(i, livenessEvery, assertAfterEachStep) {
			step.Assertions = append(step.Assertions, ValidatorOutcomeAssertions(livenessWithin, pollInterval, assertConvergence)...)
		}
		scenario.Steps = append(scenario.Steps, step)
	}

	scenario.Steps = append(scenario.Steps, AssertionStep("final quorum outcome", ValidatorOutcomeAssertions(livenessWithin, pollInterval, assertConvergence)...))
	if opts.RecoverAtEnd {
		scenario.Steps = append(scenario.Steps, Step{
			Name:       "recover all controllable faults",
			Actions:    validatorRecoveryActions(actionIDs, controller, opts.IncludeProcessFaults),
			Assertions: []Assertion{HeightAdvances(1, livenessWithin, pollInterval), ValidatorPowerRestored(recoveryPowerBaseline, livenessWithin, pollInterval), ReachabilityRestored(recoveryReachabilityBaseline, livenessWithin, pollInterval), LiveValidatorHeightsConverge(0, livenessWithin, pollInterval), NoLiveValidatorFork(), NoHeightRegression(pollInterval, pollInterval)},
			Timeout:    opts.StepTimeout,
		})
	}
	return scenario
}

func generatedChaosAssertionOptions(livenessEvery int, assertAfterEachStep, assertConvergence bool) (int, bool, bool) {
	if livenessEvery <= 0 {
		livenessEvery = 1
		assertAfterEachStep = true
	}
	if assertAfterEachStep {
		livenessEvery = 1
		assertConvergence = true
	}
	return livenessEvery, assertAfterEachStep, assertConvergence
}

func shouldAssertGeneratedChaosStep(stepIndex int, livenessEvery int, assertAfterEachStep bool) bool {
	return assertAfterEachStep || livenessEvery > 0 && (stepIndex+1)%livenessEvery == 0
}

func randomChaosAction(rng *rand.Rand, controller ValidatorChaosController, opts ValidatorChaosOptions, id NodeID, actionIDs []NodeID, livenessWithin, pollInterval time.Duration) Action {
	var actions []Action
	if opts.IncludeProcessFaults {
		bounceWait := time.Duration(0)
		if !opts.NoProcessFaultDelay {
			bounceWait = time.Duration(50+rng.Intn(250)) * time.Millisecond
		}
		actions = append(actions,
			generatedProcessOutageAction(fmt.Sprintf("bounce %s", id), []NodeID{id}, bounceWait, livenessWithin, pollInterval),
			generatedProcessOutageAction(fmt.Sprintf("restart %s", id), []NodeID{id}, 0, livenessWithin, pollInterval),
		)
		if len(actionIDs) >= 4 {
			cohort := randomMinorityCohort(rng, actionIDs)
			cohortWait := time.Duration(0)
			if !opts.NoProcessFaultDelay {
				cohortWait = time.Duration(100+rng.Intn(400)) * time.Millisecond
			}
			actions = append(actions, generatedProcessOutageAction(fmt.Sprintf("minority outage %d nodes", len(cohort)), cohort, cohortWait, livenessWithin, pollInterval))
		}
		if opts.IncludePersistentFaults {
			actions = append(actions,
				StopNode(id),
				StartNode(id),
			)
		}
	}
	if controller.Registrar != nil {
		actions = append(actions,
			generatedValidatorSetRoundTripAction(
				fmt.Sprintf("deregister and register %s", id),
				id,
				[]Action{DeregisterNodeWith(controller.Registrar, id)},
				[]Action{RegisterNodeWith(controller.Registrar, id)},
				livenessWithin,
				pollInterval,
			),
			generatedValidatorSetRoundTripAction(
				fmt.Sprintf("duplicate deregister and register %s", id),
				id,
				[]Action{DeregisterNodeWith(controller.Registrar, id), DeregisterNodeWith(controller.Registrar, id)},
				[]Action{RegisterNodeWith(controller.Registrar, id)},
				livenessWithin,
				pollInterval,
			),
		)
		if opts.IncludePersistentFaults {
			actions = append(actions,
				DeregisterNodeWith(controller.Registrar, id),
				RegisterNodeWith(controller.Registrar, id),
			)
		}
	}
	if controller.Jailer != nil {
		actions = append(actions,
			generatedValidatorSetRoundTripAction(
				fmt.Sprintf("jail and unjail %s", id),
				id,
				[]Action{JailNodeWith(controller.Jailer, id)},
				[]Action{UnjailNodeWith(controller.Jailer, id)},
				livenessWithin,
				pollInterval,
			),
		)
		if opts.IncludePersistentFaults {
			actions = append(actions,
				JailNodeWith(controller.Jailer, id),
				UnjailNodeWith(controller.Jailer, id),
			)
		}
		if controller.Registrar != nil {
			actions = append(actions,
				generatedValidatorSetRoundTripAction(
					fmt.Sprintf("jail register %s", id),
					id,
					[]Action{JailNodeWith(controller.Jailer, id)},
					[]Action{RegisterNodeWith(controller.Registrar, id)},
					livenessWithin,
					pollInterval,
				),
				generatedValidatorSetRoundTripAction(
					fmt.Sprintf("jail deregister register %s", id),
					id,
					[]Action{JailNodeWith(controller.Jailer, id), DeregisterNodeWith(controller.Registrar, id)},
					[]Action{RegisterNodeWith(controller.Registrar, id)},
					livenessWithin,
					pollInterval,
				),
			)
		}
	}
	if controller.EndpointMutator != nil {
		badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", id)
		actions = append(actions,
			Sequence(fmt.Sprintf("lie and repair endpoint %s", id), AdvertiseEndpointWith(controller.EndpointMutator, id, badEndpoint), AdvertiseEndpointWith(controller.EndpointMutator, id, "")),
		)
		if controller.Registrar != nil {
			actions = append(actions,
				Sequence(fmt.Sprintf("lie deregister register %s", id), AdvertiseEndpointWith(controller.EndpointMutator, id, badEndpoint), DeregisterNodeWith(controller.Registrar, id), RegisterNodeWith(controller.Registrar, id)),
			)
		}
		if controller.Jailer != nil {
			actions = append(actions,
				Sequence(fmt.Sprintf("jail lie repair unjail %s", id), JailNodeWith(controller.Jailer, id), AdvertiseEndpointWith(controller.EndpointMutator, id, badEndpoint), AdvertiseEndpointWith(controller.EndpointMutator, id, ""), UnjailNodeWith(controller.Jailer, id)),
			)
		}
		if opts.IncludePersistentFaults {
			actions = append(actions,
				AdvertiseEndpointWith(controller.EndpointMutator, id, badEndpoint),
				AdvertiseEndpointWith(controller.EndpointMutator, id, ""),
			)
		}
	}
	if len(actions) == 0 {
		return Wait(time.Duration(10+rng.Intn(50)) * time.Millisecond)
	}
	return actions[rng.Intn(len(actions))]
}

func generatedValidatorSetRoundTripAction(name string, id NodeID, removeActions, restoreActions []Action, within, pollInterval time.Duration) Action {
	return ActionFunc{
		Label: name,
		Fn: func(ctx context.Context, run *RunContext) error {
			activeIDs, err := activeValidatorNodeIDs(ctx, run, []NodeID{id})
			if err != nil {
				return err
			}
			availableIDs, err := availableNodeIDs(ctx, run, activeIDs)
			if err != nil {
				return err
			}
			powerBaseline := &ValidatorPowerBaseline{}
			if len(activeIDs) > 0 {
				if err := CaptureValidatorPowerBaseline(powerBaseline).Run(ctx, run); err != nil {
					return err
				}
			}
			if err := Sequence(name+" remove", removeActions...).Run(ctx, run); err != nil {
				return err
			}
			if len(activeIDs) > 0 {
				if err := checkGeneratedAssertion(ctx, run, NodesWithoutValidatorPower(activeIDs, within, pollInterval)); err != nil {
					return err
				}
				if err := checkGeneratedAssertions(ctx, run, ValidatorOutcomeAssertions(within, pollInterval, true)); err != nil {
					return err
				}
			}
			if err := Sequence(name+" restore", restoreActions...).Run(ctx, run); err != nil {
				return err
			}
			if len(activeIDs) > 0 {
				restoreAssertions := []Assertion{ValidatorPowerRestored(powerBaseline, within, pollInterval)}
				if len(availableIDs) > 0 {
					restoreAssertions = append(restoreAssertions, NodesAvailable(availableIDs, within, pollInterval))
				}
				restoreAssertions = append(restoreAssertions, ValidatorOutcomeAssertions(within, pollInterval, true)...)
				if err := checkGeneratedAssertions(ctx, run, restoreAssertions); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func generatedProcessOutageAction(name string, ids []NodeID, wait, within, pollInterval time.Duration) Action {
	ids = normalizedNodeIDs(ids)
	return ActionFunc{
		Label: name,
		Fn: func(ctx context.Context, run *RunContext) error {
			targetIDs, err := availableNodeIDs(ctx, run, ids)
			if err != nil {
				return err
			}
			if err := Parallel(name+" stop", stopActions(ids)...).Run(ctx, run); err != nil {
				return err
			}
			if len(targetIDs) > 0 {
				if err := checkGeneratedAssertion(ctx, run, NodesUnavailable(targetIDs, within, pollInterval)); err != nil {
					return err
				}
			}
			if wait > 0 {
				if err := Wait(wait).Run(ctx, run); err != nil {
					return err
				}
			}
			if err := Parallel(name+" start", startActions(ids)...).Run(ctx, run); err != nil {
				return err
			}
			if len(targetIDs) > 0 {
				if err := checkGeneratedAssertion(ctx, run, NodesAvailable(targetIDs, within, pollInterval)); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func availableNodeIDs(ctx context.Context, run *RunContext, ids []NodeID) ([]NodeID, error) {
	snapshot, err := run.Network.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	available := make([]NodeID, 0, len(ids))
	for _, id := range normalizedNodeIDs(ids) {
		node, ok := snapshot.ByNode(id)
		if !ok {
			continue
		}
		if node.Reachable && node.Ready && node.Live {
			available = append(available, id)
		}
	}
	return available, nil
}

func activeValidatorNodeIDs(ctx context.Context, run *RunContext, ids []NodeID) ([]NodeID, error) {
	snapshot, err := run.Network.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	active := make([]NodeID, 0, len(ids))
	for _, id := range normalizedNodeIDs(ids) {
		node, ok := snapshot.ByNode(id)
		if !ok {
			continue
		}
		if node.Live && node.ValidatorPower > 0 {
			active = append(active, id)
		}
	}
	return active, nil
}

func checkGeneratedAssertion(ctx context.Context, run *RunContext, assertion Assertion) error {
	if assertion == nil {
		return nil
	}
	run.record("assertion_start", assertion.Name(), "")
	if err := assertion.Check(ctx, run); err != nil {
		run.record("assertion_fail", assertion.Name(), err.Error())
		return fmt.Errorf("%s: %w", assertion.Name(), err)
	}
	run.record("assertion_pass", assertion.Name(), "")
	return nil
}

func checkGeneratedAssertions(ctx context.Context, run *RunContext, assertions []Assertion) error {
	for _, assertion := range assertions {
		if err := checkGeneratedAssertion(ctx, run, assertion); err != nil {
			return err
		}
	}
	return nil
}

func randomMinorityCohort(rng *rand.Rand, ids []NodeID) []NodeID {
	limit := len(ids) / 3
	if limit <= 0 {
		limit = 1
	}
	size := 1 + rng.Intn(limit)
	perm := rng.Perm(len(ids))
	cohort := make([]NodeID, 0, size)
	for _, index := range perm[:size] {
		cohort = append(cohort, ids[index])
	}
	return cohort
}

func validatorRecoveryActions(ids []NodeID, controller ValidatorChaosController, includeProcesses bool) []Action {
	var stages []Action
	if controller.EndpointMutator != nil {
		stages = append(stages, Sequence("repair all endpoints", endpointRepairActions(controller.EndpointMutator, ids)...))
	}
	if controller.Jailer != nil {
		stages = append(stages, Sequence("unjail all validators", unjailActions(controller.Jailer, ids)...))
	}
	if controller.Registrar != nil {
		stages = append(stages, Sequence("register all validators", registerActions(controller.Registrar, ids)...))
	}
	if includeProcesses {
		stages = append(stages, Sequence("start all nodes", startActions(ids)...))
	}
	if len(stages) == 0 {
		return nil
	}
	return []Action{Sequence("recover all controllable validator faults", stages...)}
}
