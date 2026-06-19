package fuzz

import (
	"fmt"
	"time"
)

const defaultSimulatedProgramMaxSteps = 1_000

type SimulatedProgramOptions struct {
	MaxSteps                int
	LivenessEvery           int
	LivenessWithin          time.Duration
	PollInterval            time.Duration
	AssertAfterEachStep     bool
	AssertConvergence       bool
	IncludePersistentFaults bool
	RecoverAtEnd            bool
}

func SimulatedChaosScenarioFromProgram(spec NetworkSpec, controller ValidatorChaosController, program []byte, opts SimulatedProgramOptions) Scenario {
	if len(program) == 0 {
		program = []byte{0}
	}
	maxSteps := opts.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultSimulatedProgramMaxSteps
	}
	if maxSteps > len(program) {
		maxSteps = len(program)
	}
	livenessEvery := opts.LivenessEvery
	assertAfterEachStep := opts.AssertAfterEachStep
	assertConvergence := opts.AssertConvergence
	livenessEvery, assertAfterEachStep, assertConvergence = generatedChaosAssertionOptions(livenessEvery, assertAfterEachStep, assertConvergence)
	livenessWithin := opts.LivenessWithin
	if livenessWithin <= 0 {
		livenessWithin = time.Second
	}
	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = time.Millisecond
	}

	ids := spec.NodeIDs()
	scenario := Scenario{
		Name: "simulated-program-chaos",
		Steps: []Step{
			{
				Name:       "initial validator outcome",
				Assertions: append([]Assertion{AllReachable()}, ValidatorOutcomeAssertions(livenessWithin, pollInterval, assertConvergence)...),
			},
		},
	}
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
	for i := 0; i < maxSteps; i++ {
		step := Step{
			Name:    fmt.Sprintf("program chaos %04d", i+1),
			Actions: []Action{programAction(program, i, ids, controller, opts.IncludePersistentFaults, livenessWithin, pollInterval)},
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
			Actions:    validatorRecoveryActions(ids, controller, true),
			Assertions: []Assertion{HeightAdvances(1, livenessWithin, pollInterval), ValidatorPowerRestored(recoveryPowerBaseline, livenessWithin, pollInterval), ReachabilityRestored(recoveryReachabilityBaseline, livenessWithin, pollInterval), LiveValidatorHeightsConverge(0, livenessWithin, pollInterval), NoLiveValidatorFork(), NoHeightRegression(pollInterval, pollInterval)},
		})
	}
	return scenario
}

func programAction(program []byte, offset int, ids []NodeID, controller ValidatorChaosController, includePersistentFaults bool, livenessWithin, pollInterval time.Duration) Action {
	if len(ids) == 0 {
		return Wait(0)
	}
	id := ids[programNodeIndex(program, offset)%len(ids)]
	if includePersistentFaults {
		if controllerAction := persistentProgramAction(program, offset, id, controller); controllerAction != nil {
			return controllerAction
		}
	}
	switch program[offset] % 11 {
	case 0:
		return generatedProcessOutageAction(fmt.Sprintf("program bounce %s", id), []NodeID{id}, 0, livenessWithin, pollInterval)
	case 1:
		return generatedProcessOutageAction(fmt.Sprintf("program restart %s", id), []NodeID{id}, 0, livenessWithin, pollInterval)
	case 2:
		return generatedValidatorSetRoundTripAction(
			fmt.Sprintf("program deregister/register %s", id),
			id,
			[]Action{DeregisterNodeWith(controller.Registrar, id)},
			[]Action{RegisterNodeWith(controller.Registrar, id)},
			livenessWithin,
			pollInterval,
		)
	case 3:
		return generatedValidatorSetRoundTripAction(
			fmt.Sprintf("program duplicate deregister/register %s", id),
			id,
			[]Action{DeregisterNodeWith(controller.Registrar, id), DeregisterNodeWith(controller.Registrar, id)},
			[]Action{RegisterNodeWith(controller.Registrar, id)},
			livenessWithin,
			pollInterval,
		)
	case 4:
		return generatedValidatorSetRoundTripAction(
			fmt.Sprintf("program jail/unjail %s", id),
			id,
			[]Action{JailNodeWith(controller.Jailer, id)},
			[]Action{UnjailNodeWith(controller.Jailer, id)},
			livenessWithin,
			pollInterval,
		)
	case 5:
		return generatedValidatorSetRoundTripAction(
			fmt.Sprintf("program jail/register %s", id),
			id,
			[]Action{JailNodeWith(controller.Jailer, id)},
			[]Action{RegisterNodeWith(controller.Registrar, id)},
			livenessWithin,
			pollInterval,
		)
	case 6:
		return generatedValidatorSetRoundTripAction(
			fmt.Sprintf("program jail/deregister/register %s", id),
			id,
			[]Action{JailNodeWith(controller.Jailer, id), DeregisterNodeWith(controller.Registrar, id)},
			[]Action{RegisterNodeWith(controller.Registrar, id)},
			livenessWithin,
			pollInterval,
		)
	case 7:
		badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", id)
		return Sequence(fmt.Sprintf("program lie/repair %s", id), AdvertiseEndpointWith(controller.EndpointMutator, id, badEndpoint), AdvertiseEndpointWith(controller.EndpointMutator, id, ""))
	case 8:
		badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", id)
		return Sequence(fmt.Sprintf("program lie/deregister/register %s", id), AdvertiseEndpointWith(controller.EndpointMutator, id, badEndpoint), DeregisterNodeWith(controller.Registrar, id), RegisterNodeWith(controller.Registrar, id))
	case 9:
		badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", id)
		return Sequence(fmt.Sprintf("program jail/lie/repair/unjail %s", id), JailNodeWith(controller.Jailer, id), AdvertiseEndpointWith(controller.EndpointMutator, id, badEndpoint), AdvertiseEndpointWith(controller.EndpointMutator, id, ""), UnjailNodeWith(controller.Jailer, id))
	default:
		return programMinorityOutage(program, offset, ids, livenessWithin, pollInterval)
	}
}

func persistentProgramAction(program []byte, offset int, id NodeID, controller ValidatorChaosController) Action {
	if program[offset]%2 == 0 {
		return nil
	}
	switch program[(offset+3)%len(program)] % 10 {
	case 0:
		return StopNode(id)
	case 1:
		return StartNode(id)
	case 2:
		if controller.Registrar == nil {
			return nil
		}
		return DeregisterNodeWith(controller.Registrar, id)
	case 3:
		if controller.Registrar == nil {
			return nil
		}
		return RegisterNodeWith(controller.Registrar, id)
	case 4:
		if controller.Jailer == nil {
			return nil
		}
		return JailNodeWith(controller.Jailer, id)
	case 5:
		if controller.Jailer == nil {
			return nil
		}
		return UnjailNodeWith(controller.Jailer, id)
	case 6:
		if controller.EndpointMutator == nil {
			return nil
		}
		badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", id)
		return AdvertiseEndpointWith(controller.EndpointMutator, id, badEndpoint)
	case 7:
		if controller.EndpointMutator == nil {
			return nil
		}
		return AdvertiseEndpointWith(controller.EndpointMutator, id, "")
	case 8:
		if controller.Registrar == nil {
			return nil
		}
		return Sequence(fmt.Sprintf("program persistent duplicate deregister %s", id), DeregisterNodeWith(controller.Registrar, id), DeregisterNodeWith(controller.Registrar, id))
	default:
		return nil
	}
}

func programMinorityOutage(program []byte, offset int, ids []NodeID, livenessWithin, pollInterval time.Duration) Action {
	limit := len(ids) / 3
	if limit <= 0 {
		limit = 1
	}
	size := 1 + int(program[(offset+2)%len(program)])%limit
	cohort := make([]NodeID, 0, size)
	seen := map[NodeID]struct{}{}
	for i := 0; len(cohort) < size && i < len(ids)*2; i++ {
		id := ids[(programNodeIndex(program, offset+i)+i)%len(ids)]
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		cohort = append(cohort, id)
	}
	return generatedProcessOutageAction(fmt.Sprintf("program minority outage %d nodes", len(cohort)), cohort, 0, livenessWithin, pollInterval)
}
