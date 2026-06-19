package fuzz

import (
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
	Seed                 int64
	Steps                int
	StepTimeout          time.Duration
	LivenessEvery        int
	LivenessWithin       time.Duration
	PollInterval         time.Duration
	ActionNodeIDs        []NodeID
	StartNodes           bool
	IncludeProcessFaults bool
	NoProcessFaultDelay  bool
	AssertAfterEachStep  bool
}

func ValidatorChaosScenario(spec NetworkSpec, controller ValidatorChaosController, opts ValidatorChaosOptions) Scenario {
	steps := clamp(opts.Steps, 1, defaultModelStepLimit)
	livenessEvery := opts.LivenessEvery
	if livenessEvery <= 0 {
		livenessEvery = 25
	}
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
		AssertionStep("initial reachability", AllReachable()),
		AssertionStep("initial liveness", HeightAdvances(1, livenessWithin, pollInterval)),
	)

	for i := 0; i < steps; i++ {
		step := Step{
			Name:    fmt.Sprintf("chaos %04d", i+1),
			Timeout: opts.StepTimeout,
		}
		if len(actionIDs) > 0 {
			id := actionIDs[rng.Intn(len(actionIDs))]
			step.Actions = append(step.Actions, randomChaosAction(rng, controller, opts, id, actionIDs))
		}
		if opts.AssertAfterEachStep || (i+1)%livenessEvery == 0 {
			step.Assertions = append(step.Assertions, HeightFollowsValidatorQuorum(livenessWithin, pollInterval))
		}
		if (i+1)%livenessEvery == 0 {
			step.Assertions = append(step.Assertions, NoHeightRegression(pollInterval, pollInterval))
		}
		scenario.Steps = append(scenario.Steps, step)
	}

	scenario.Steps = append(scenario.Steps, AssertionStep("final quorum outcome", HeightFollowsValidatorQuorum(livenessWithin, pollInterval)))
	return scenario
}

func randomChaosAction(rng *rand.Rand, controller ValidatorChaosController, opts ValidatorChaosOptions, id NodeID, actionIDs []NodeID) Action {
	var actions []Action
	if opts.IncludeProcessFaults {
		bounceActions := []Action{StopNode(id)}
		if !opts.NoProcessFaultDelay {
			bounceActions = append(bounceActions, Wait(time.Duration(50+rng.Intn(250))*time.Millisecond))
		}
		bounceActions = append(bounceActions, StartNode(id))
		actions = append(actions,
			Sequence(fmt.Sprintf("bounce %s", id), bounceActions...),
			RestartNode(id),
		)
		if len(actionIDs) >= 4 {
			cohort := randomMinorityCohort(rng, actionIDs)
			stop := make([]Action, 0, len(cohort))
			start := make([]Action, 0, len(cohort))
			for _, cohortID := range cohort {
				stop = append(stop, StopNode(cohortID))
				start = append(start, StartNode(cohortID))
			}
			partitionActions := []Action{Parallel("stop minority cohort", stop...)}
			if !opts.NoProcessFaultDelay {
				partitionActions = append(partitionActions, Wait(time.Duration(100+rng.Intn(400))*time.Millisecond))
			}
			partitionActions = append(partitionActions, Parallel("start minority cohort", start...))
			actions = append(actions, Sequence(fmt.Sprintf("minority outage %d nodes", len(cohort)), partitionActions...))
		}
	}
	if controller.Registrar != nil {
		actions = append(actions,
			Sequence(fmt.Sprintf("deregister and register %s", id), DeregisterNodeWith(controller.Registrar, id), RegisterNodeWith(controller.Registrar, id)),
			Sequence(fmt.Sprintf("duplicate deregister and register %s", id), DeregisterNodeWith(controller.Registrar, id), DeregisterNodeWith(controller.Registrar, id), RegisterNodeWith(controller.Registrar, id)),
		)
	}
	if controller.Jailer != nil {
		actions = append(actions,
			Sequence(fmt.Sprintf("jail and unjail %s", id), JailNodeWith(controller.Jailer, id), UnjailNodeWith(controller.Jailer, id)),
		)
		if controller.Registrar != nil {
			actions = append(actions,
				Sequence(fmt.Sprintf("jail deregister register %s", id), JailNodeWith(controller.Jailer, id), DeregisterNodeWith(controller.Registrar, id), RegisterNodeWith(controller.Registrar, id)),
			)
		}
	}
	if controller.EndpointMutator != nil {
		badEndpoint := fmt.Sprintf("https://wrong-%s.oap.invalid", id)
		actions = append(actions,
			Sequence(fmt.Sprintf("lie and repair endpoint %s", id), AdvertiseEndpointWith(controller.EndpointMutator, id, badEndpoint), AdvertiseEndpointWith(controller.EndpointMutator, id, "")),
		)
	}
	if len(actions) == 0 {
		return Wait(time.Duration(10+rng.Intn(50)) * time.Millisecond)
	}
	return actions[rng.Intn(len(actions))]
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
