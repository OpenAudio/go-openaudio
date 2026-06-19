package fuzz

import (
	"fmt"
	"time"
)

const defaultSimulatedProgramMaxSteps = 1_000

type SimulatedProgramOptions struct {
	MaxSteps       int
	LivenessEvery  int
	LivenessWithin time.Duration
	PollInterval   time.Duration
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
	if livenessEvery <= 0 {
		livenessEvery = 25
	}
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
			AssertionStep("initial reachability", AllReachable()),
			AssertionStep("initial liveness", HeightAdvances(1, livenessWithin, pollInterval)),
		},
	}
	for i := 0; i < maxSteps; i++ {
		step := Step{
			Name:    fmt.Sprintf("program chaos %04d", i+1),
			Actions: []Action{programAction(program, i, ids, controller)},
			Timeout: livenessWithin,
		}
		if (i+1)%livenessEvery == 0 {
			step.Assertions = append(step.Assertions, HeightAdvances(1, livenessWithin, pollInterval))
			step.Assertions = append(step.Assertions, NoHeightRegression(pollInterval, pollInterval))
		}
		scenario.Steps = append(scenario.Steps, step)
	}
	scenario.Steps = append(scenario.Steps, AssertionStep("final liveness", HeightAdvances(1, livenessWithin, pollInterval)))
	return scenario
}

func programAction(program []byte, offset int, ids []NodeID, controller ValidatorChaosController) Action {
	if len(ids) == 0 {
		return Wait(0)
	}
	id := ids[programNodeIndex(program, offset)%len(ids)]
	switch program[offset] % 8 {
	case 0:
		return Sequence(fmt.Sprintf("program bounce %s", id), StopNode(id), StartNode(id))
	case 1:
		return RestartNode(id)
	case 2:
		return Sequence(fmt.Sprintf("program deregister/register %s", id), DeregisterNodeWith(controller.Registrar, id), RegisterNodeWith(controller.Registrar, id))
	case 3:
		return Sequence(fmt.Sprintf("program duplicate deregister/register %s", id), DeregisterNodeWith(controller.Registrar, id), DeregisterNodeWith(controller.Registrar, id), RegisterNodeWith(controller.Registrar, id))
	case 4:
		return Sequence(fmt.Sprintf("program jail/unjail %s", id), JailNodeWith(controller.Jailer, id), UnjailNodeWith(controller.Jailer, id))
	case 5:
		return Sequence(fmt.Sprintf("program jail/deregister/register %s", id), JailNodeWith(controller.Jailer, id), DeregisterNodeWith(controller.Registrar, id), RegisterNodeWith(controller.Registrar, id))
	case 6:
		badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", id)
		return Sequence(fmt.Sprintf("program lie/repair %s", id), AdvertiseEndpointWith(controller.EndpointMutator, id, badEndpoint), AdvertiseEndpointWith(controller.EndpointMutator, id, ""))
	default:
		return programMinorityOutage(program, offset, ids)
	}
}

func programMinorityOutage(program []byte, offset int, ids []NodeID) Action {
	limit := len(ids) / 3
	if limit <= 0 {
		limit = 1
	}
	size := 1 + int(program[(offset+2)%len(program)])%limit
	stop := make([]Action, 0, size)
	start := make([]Action, 0, size)
	seen := map[NodeID]struct{}{}
	for i := 0; len(stop) < size && i < len(ids)*2; i++ {
		id := ids[(programNodeIndex(program, offset+i)+i)%len(ids)]
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		stop = append(stop, StopNode(id))
		start = append(start, StartNode(id))
	}
	return Sequence(fmt.Sprintf("program minority outage %d nodes", len(stop)), Parallel("program stop minority", stop...), Parallel("program start minority", start...))
}
