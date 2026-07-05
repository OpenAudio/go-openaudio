package fuzz

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

type Scenario struct {
	Name  string
	Seed  int64
	Steps []Step
}

type Step struct {
	Name       string
	Actions    []Action
	Assertions []Assertion
	Timeout    time.Duration
}

func ActionStep(name string, actions ...Action) Step {
	return Step{Name: name, Actions: actions}
}

func AssertionStep(name string, assertions ...Assertion) Step {
	return Step{Name: name, Assertions: assertions}
}

type Runner struct {
	Network     Network
	Seed        int64
	StepTimeout time.Duration
	EventSink   func(Event)
}

type RunContext struct {
	Network Network
	Rand    *rand.Rand

	mu        sync.Mutex
	events    []Event
	eventSink func(Event)
}

type Event struct {
	Time    time.Time
	Kind    string
	Message string
	Detail  string
}

type Result struct {
	ScenarioName string
	Seed         int64
	StartedAt    time.Time
	FinishedAt   time.Time
	Passed       bool
	Error        string
	Events       []Event
}

func (r Runner) Run(ctx context.Context, scenario Scenario) (Result, error) {
	if r.Network == nil {
		return Result{}, fmt.Errorf("%w: network is required", ErrInvalidScenario)
	}
	if scenario.Name == "" {
		return Result{}, fmt.Errorf("%w: scenario name is required", ErrInvalidScenario)
	}
	if len(scenario.Steps) == 0 {
		return Result{}, fmt.Errorf("%w: at least one step is required", ErrInvalidScenario)
	}

	seed := scenario.Seed
	if seed == 0 {
		seed = r.Seed
	}
	if seed == 0 {
		seed = time.Now().UnixNano()
	}

	run := &RunContext{
		Network:   r.Network,
		Rand:      rand.New(rand.NewSource(seed)),
		eventSink: r.EventSink,
	}
	result := Result{
		ScenarioName: scenario.Name,
		Seed:         seed,
		StartedAt:    time.Now().UTC(),
	}
	run.record("scenario_start", scenario.Name, fmt.Sprintf("seed=%d", seed))

	for i, step := range scenario.Steps {
		stepName := step.Name
		if stepName == "" {
			stepName = fmt.Sprintf("step %d", i+1)
		}
		timeout := step.Timeout
		if timeout <= 0 {
			timeout = r.StepTimeout
		}
		if timeout <= 0 {
			timeout = 30 * time.Second
		}

		stepCtx, cancel := context.WithTimeout(ctx, timeout)
		run.record("step_start", stepName, fmt.Sprintf("timeout=%s", timeout))
		err := runStep(stepCtx, run, step)
		cancel()
		if err != nil {
			run.record("step_fail", stepName, err.Error())
			result.FinishedAt = time.Now().UTC()
			result.Events = run.Events()
			result.Error = err.Error()
			return result, err
		}
		run.record("step_pass", stepName, "")
	}

	run.record("scenario_pass", scenario.Name, "")
	result.FinishedAt = time.Now().UTC()
	result.Passed = true
	result.Events = run.Events()
	return result, nil
}

func runStep(ctx context.Context, run *RunContext, step Step) error {
	for _, action := range step.Actions {
		if action == nil {
			continue
		}
		run.record("action_start", action.Name(), "")
		if err := action.Run(ctx, run); err != nil {
			return fmt.Errorf("%s: %w", action.Name(), err)
		}
		run.record("action_pass", action.Name(), "")
	}
	for _, assertion := range step.Assertions {
		if assertion == nil {
			continue
		}
		run.record("assertion_start", assertion.Name(), "")
		if err := assertion.Check(ctx, run); err != nil {
			return fmt.Errorf("%s: %w", assertion.Name(), err)
		}
		run.record("assertion_pass", assertion.Name(), "")
	}
	return nil
}

func (r *RunContext) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()

	events := make([]Event, len(r.events))
	copy(events, r.events)
	return events
}

func (r *RunContext) record(kind, message, detail string) {
	event := Event{
		Time:    time.Now().UTC(),
		Kind:    kind,
		Message: message,
		Detail:  detail,
	}

	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()

	if r.eventSink != nil {
		r.eventSink(event)
	}
}
