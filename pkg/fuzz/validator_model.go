package fuzz

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
)

const (
	DefaultModelNodeLimit  = 300
	defaultModelNodePower  = int64(10)
	defaultModelStepLimit  = 20_000
	modelTraceEventLimit   = 12
	modelDefaultNodePrefix = "node"
)

type ValidatorSetBehavior int

const (
	// ValidatorSetBehaviorCurrent models the intended behavior: deregistration
	// emits a Comet zero-power update only when the validator is active.
	ValidatorSetBehaviorCurrent ValidatorSetBehavior = iota
	// ValidatorSetBehaviorBuggyJailedDeregistration models the pre-fix class of
	// bug where deregistration emits a zero-power update even for a jailed row.
	ValidatorSetBehaviorBuggyJailedDeregistration
	// ValidatorSetBehaviorBuggyAnyDeregistrationUpdate models duplicate or
	// absent-validator removals emitting repeated zero-power updates.
	ValidatorSetBehaviorBuggyAnyDeregistrationUpdate
	// ValidatorSetBehaviorBuggyRegisterWithoutCometUpdate models app state
	// becoming active while the Comet validator set is not updated.
	ValidatorSetBehaviorBuggyRegisterWithoutCometUpdate
	// ValidatorSetBehaviorBuggyRegisterNoop models recovery code that accepts a
	// register action but leaves a removed validator out of the validator set.
	ValidatorSetBehaviorBuggyRegisterNoop
	// ValidatorSetBehaviorBuggyStartAbsentOnline models lifecycle code marking
	// an absent or jailed validator online.
	ValidatorSetBehaviorBuggyStartAbsentOnline
	// ValidatorSetBehaviorBuggyTickWithoutQuorum models height advancement when
	// the active online validator power does not have consensus quorum.
	ValidatorSetBehaviorBuggyTickWithoutQuorum
	// ValidatorSetBehaviorBuggyStallWithQuorum models a halt despite enough
	// online active validator power to make progress.
	ValidatorSetBehaviorBuggyStallWithQuorum
)

type ModelValidatorState int

const (
	ModelValidatorAbsent ModelValidatorState = iota
	ModelValidatorActive
	ModelValidatorJailed
)

func (s ModelValidatorState) String() string {
	switch s {
	case ModelValidatorAbsent:
		return "absent"
	case ModelValidatorActive:
		return "active"
	case ModelValidatorJailed:
		return "jailed"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

type ModelActionKind uint8

const (
	ModelRegister ModelActionKind = iota
	ModelJail
	ModelUnjail
	ModelDeregister
	ModelDeregisterTwice
	ModelStop
	ModelStart
	ModelLieEndpoint
	ModelRepairEndpoint
	ModelTick
	modelActionCount
)

func (a ModelActionKind) String() string {
	switch a {
	case ModelRegister:
		return "register"
	case ModelJail:
		return "jail"
	case ModelUnjail:
		return "unjail"
	case ModelDeregister:
		return "deregister"
	case ModelDeregisterTwice:
		return "deregister_twice"
	case ModelStop:
		return "stop"
	case ModelStart:
		return "start"
	case ModelLieEndpoint:
		return "lie_endpoint"
	case ModelRepairEndpoint:
		return "repair_endpoint"
	case ModelTick:
		return "tick"
	default:
		return fmt.Sprintf("unknown(%d)", a)
	}
}

type ModelAction struct {
	Kind ModelActionKind
	Node NodeID
}

type ModelNode struct {
	ID             NodeID
	State          ModelValidatorState
	InCometSet     bool
	Online         bool
	EndpointHonest bool
	Power          int64
}

type ValidatorModelOptions struct {
	NodeCount     int
	InitialActive int
	Behavior      ValidatorSetBehavior
	NodePowers    map[NodeID]int64
}

type ValidatorLifecycleModel struct {
	Behavior ValidatorSetBehavior
	Nodes    map[NodeID]*ModelNode
	Order    []NodeID
	Height   int64
	Events   []ModelEvent
}

type ModelEvent struct {
	Step          int
	Action        ModelActionKind
	Node          NodeID
	StateBefore   ModelValidatorState
	StateAfter    ModelValidatorState
	InCometBefore bool
	InCometAfter  bool
	OnlineBefore  bool
	OnlineAfter   bool
	EmittedUpdate bool
	QuorumBefore  bool
	QuorumAfter   bool
	TotalPower    int64
	OnlinePower   int64
	Height        int64
	Error         string
}

type ModelResult struct {
	Seed      int64
	NodeCount int
	Steps     int
	Height    int64
	Events    []ModelEvent
}

func NewValidatorLifecycleModel(opts ValidatorModelOptions) *ValidatorLifecycleModel {
	nodeCount := clamp(opts.NodeCount, 1, DefaultModelNodeLimit)
	initialActive := opts.InitialActive
	if initialActive <= 0 || initialActive > nodeCount {
		initialActive = nodeCount
	}

	model := &ValidatorLifecycleModel{
		Behavior: opts.Behavior,
		Nodes:    make(map[NodeID]*ModelNode, nodeCount),
		Order:    make([]NodeID, 0, nodeCount),
	}
	for i := 0; i < nodeCount; i++ {
		id := NodeID(fmt.Sprintf("%s%d", modelDefaultNodePrefix, i+1))
		power := opts.NodePowers[id]
		if power <= 0 {
			power = defaultModelNodePower
		}
		node := &ModelNode{
			ID:             id,
			State:          ModelValidatorAbsent,
			EndpointHonest: true,
			Power:          power,
		}
		if i < initialActive {
			node.State = ModelValidatorActive
			node.InCometSet = true
			node.Online = true
		}
		model.Nodes[id] = node
		model.Order = append(model.Order, id)
	}
	return model
}

func (m *ValidatorLifecycleModel) Apply(step int, action ModelAction) error {
	if action.Kind == ModelTick {
		return m.tick(step, action)
	}

	node := m.Nodes[action.Node]
	if node == nil {
		return fmt.Errorf("%w: %s", ErrNodeNotFound, action.Node)
	}

	event := ModelEvent{
		Step:          step,
		Action:        action.Kind,
		Node:          action.Node,
		StateBefore:   node.State,
		InCometBefore: node.InCometSet,
		OnlineBefore:  node.Online,
		QuorumBefore:  m.HasOnlineQuorum(),
		Height:        m.Height,
	}
	event.TotalPower, event.OnlinePower = m.Power()

	switch action.Kind {
	case ModelRegister:
		if m.Behavior == ValidatorSetBehaviorBuggyRegisterNoop {
			event.EmittedUpdate = false
			break
		}
		node.State = ModelValidatorActive
		node.Online = true
		node.EndpointHonest = true
		if m.Behavior == ValidatorSetBehaviorBuggyRegisterWithoutCometUpdate {
			node.InCometSet = false
			event.EmittedUpdate = false
		} else {
			node.InCometSet = true
			event.EmittedUpdate = true
		}
	case ModelJail:
		if node.State == ModelValidatorActive {
			event.EmittedUpdate = true
			if !node.InCometSet {
				return m.recordInvalid(event, "jail emitted zero-power update for validator missing from comet set")
			}
			node.State = ModelValidatorJailed
			node.InCometSet = false
			node.Online = false
		}
	case ModelUnjail:
		if node.State == ModelValidatorJailed {
			node.State = ModelValidatorActive
			node.InCometSet = true
			node.Online = true
			event.EmittedUpdate = true
		}
	case ModelDeregister:
		if err := m.deregister(node, &event); err != nil {
			return err
		}
	case ModelDeregisterTwice:
		if err := m.deregister(node, &event); err != nil {
			return err
		}
		if err := m.finishEvent(&event, node); err != nil {
			return err
		}
		second := ModelEvent{
			Step:          step,
			Action:        action.Kind,
			Node:          action.Node,
			StateBefore:   node.State,
			InCometBefore: node.InCometSet,
			OnlineBefore:  node.Online,
			QuorumBefore:  m.HasOnlineQuorum(),
			Height:        m.Height,
		}
		second.TotalPower, second.OnlinePower = m.Power()
		if err := m.deregister(node, &second); err != nil {
			return err
		}
		return m.finishEvent(&second, node)
	case ModelStop:
		node.Online = false
	case ModelStart:
		if node.State == ModelValidatorActive || m.Behavior == ValidatorSetBehaviorBuggyStartAbsentOnline {
			node.Online = true
		}
	case ModelLieEndpoint:
		if node.State != ModelValidatorAbsent {
			node.EndpointHonest = false
		}
	case ModelRepairEndpoint:
		node.EndpointHonest = true
	default:
		return fmt.Errorf("unsupported model action %d", action.Kind)
	}

	return m.finishEvent(&event, node)
}

func (m *ValidatorLifecycleModel) deregister(node *ModelNode, event *ModelEvent) error {
	emitUpdate := false
	switch m.Behavior {
	case ValidatorSetBehaviorCurrent:
		emitUpdate = node.State == ModelValidatorActive
	case ValidatorSetBehaviorBuggyJailedDeregistration:
		emitUpdate = node.State == ModelValidatorActive || node.State == ModelValidatorJailed
	case ValidatorSetBehaviorBuggyAnyDeregistrationUpdate:
		emitUpdate = true
	case ValidatorSetBehaviorBuggyRegisterWithoutCometUpdate,
		ValidatorSetBehaviorBuggyRegisterNoop,
		ValidatorSetBehaviorBuggyStartAbsentOnline,
		ValidatorSetBehaviorBuggyTickWithoutQuorum,
		ValidatorSetBehaviorBuggyStallWithQuorum:
		emitUpdate = node.State == ModelValidatorActive
	default:
		return fmt.Errorf("unsupported validator set behavior %d", m.Behavior)
	}

	event.EmittedUpdate = emitUpdate
	if emitUpdate && !node.InCometSet {
		return m.recordInvalid(*event, "deregistration emitted zero-power update for validator missing from comet set")
	}

	node.State = ModelValidatorAbsent
	node.InCometSet = false
	node.Online = false
	node.EndpointHonest = true
	return nil
}

func (m *ValidatorLifecycleModel) tick(step int, action ModelAction) error {
	quorumBefore := m.HasOnlineQuorum()
	heightBefore := m.Height
	event := ModelEvent{
		Step:         step,
		Action:       action.Kind,
		Node:         action.Node,
		Height:       m.Height,
		QuorumBefore: quorumBefore,
	}
	event.TotalPower, event.OnlinePower = m.Power()

	advance := quorumBefore
	switch m.Behavior {
	case ValidatorSetBehaviorBuggyTickWithoutQuorum:
		advance = true
	case ValidatorSetBehaviorBuggyStallWithQuorum:
		advance = false
	}
	if advance {
		m.Height++
	}
	event.Height = m.Height
	event.QuorumAfter = m.HasOnlineQuorum()
	m.Events = append(m.Events, event)
	if !quorumBefore && m.Height > heightBefore {
		return m.recordInvalid(event, "height advanced without online quorum")
	}
	if quorumBefore && m.Height == heightBefore {
		return m.recordInvalid(event, "height stalled despite online quorum")
	}
	return m.validateState(event)
}

func (m *ValidatorLifecycleModel) Power() (totalPower, onlinePower int64) {
	for _, node := range m.Nodes {
		if !node.InCometSet {
			continue
		}
		totalPower += node.Power
		if node.Online {
			onlinePower += node.Power
		}
	}
	return totalPower, onlinePower
}

func (m *ValidatorLifecycleModel) HasOnlineQuorum() bool {
	totalPower, onlinePower := m.Power()
	return totalPower > 0 && onlinePower*3 > totalPower*2
}

func (m *ValidatorLifecycleModel) finishEvent(event *ModelEvent, node *ModelNode) error {
	event.StateAfter = node.State
	event.InCometAfter = node.InCometSet
	event.OnlineAfter = node.Online
	event.QuorumAfter = m.HasOnlineQuorum()
	event.TotalPower, event.OnlinePower = m.Power()
	event.Height = m.Height
	m.Events = append(m.Events, *event)
	return m.validateState(*event)
}

func (m *ValidatorLifecycleModel) recordInvalid(event ModelEvent, msg string) error {
	event.Error = msg
	m.Events = append(m.Events, event)
	return &ModelInvariantError{
		Message: msg,
		Event:   event,
		Trace:   m.RecentEvents(modelTraceEventLimit),
	}
}

func (m *ValidatorLifecycleModel) validateState(event ModelEvent) error {
	for _, node := range m.Nodes {
		if node.State == ModelValidatorActive && !node.InCometSet {
			return m.recordInvalid(event, fmt.Sprintf("%s is active in app state but absent from comet set", node.ID))
		}
		if node.State != ModelValidatorActive && node.InCometSet {
			return m.recordInvalid(event, fmt.Sprintf("%s is %s in app state but still in comet set", node.ID, node.State))
		}
		if node.Online && node.State != ModelValidatorActive {
			return m.recordInvalid(event, fmt.Sprintf("%s is online while %s", node.ID, node.State))
		}
	}
	return nil
}

func (m *ValidatorLifecycleModel) RecentEvents(limit int) []ModelEvent {
	if limit <= 0 || len(m.Events) <= limit {
		return append([]ModelEvent{}, m.Events...)
	}
	return append([]ModelEvent{}, m.Events[len(m.Events)-limit:]...)
}

type ModelInvariantError struct {
	Message string
	Event   ModelEvent
	Trace   []ModelEvent
}

func (e *ModelInvariantError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s at step %d action=%s node=%s state=%s in_comet=%t",
		e.Message,
		e.Event.Step,
		e.Event.Action,
		e.Event.Node,
		e.Event.StateBefore,
		e.Event.InCometBefore,
	)
	for _, event := range e.Trace {
		fmt.Fprintf(&b, "\n  step=%d action=%s node=%s %s->%s comet=%t->%t online=%t->%t quorum=%t->%t power=%d/%d emitted=%t height=%d",
			event.Step,
			event.Action,
			event.Node,
			event.StateBefore,
			event.StateAfter,
			event.InCometBefore,
			event.InCometAfter,
			event.OnlineBefore,
			event.OnlineAfter,
			event.QuorumBefore,
			event.QuorumAfter,
			event.OnlinePower,
			event.TotalPower,
			event.EmittedUpdate,
			event.Height,
		)
		if event.Error != "" {
			fmt.Fprintf(&b, " error=%q", event.Error)
		}
	}
	return b.String()
}

func RunValidatorLifecycleModel(seed int64, nodeCount, steps int, behavior ValidatorSetBehavior) (ModelResult, error) {
	nodeCount = clamp(nodeCount, 1, DefaultModelNodeLimit)
	steps = clamp(steps, 1, defaultModelStepLimit)
	model := NewValidatorLifecycleModel(ValidatorModelOptions{
		NodeCount:     nodeCount,
		InitialActive: nodeCount,
		Behavior:      behavior,
	})
	rng := rand.New(rand.NewSource(seed))

	for step := 0; step < steps; step++ {
		action := randomModelAction(rng, model.Order)
		if err := model.Apply(step, action); err != nil {
			return ModelResult{
				Seed:      seed,
				NodeCount: nodeCount,
				Steps:     step + 1,
				Height:    model.Height,
				Events:    model.Events,
			}, err
		}
	}

	return ModelResult{
		Seed:      seed,
		NodeCount: nodeCount,
		Steps:     steps,
		Height:    model.Height,
		Events:    model.Events,
	}, nil
}

func RunValidatorLifecycleProgram(program []byte, nodeCount int, behavior ValidatorSetBehavior) (ModelResult, error) {
	nodeCount = clamp(nodeCount, 1, DefaultModelNodeLimit)
	if len(program) == 0 {
		program = []byte{byte(ModelTick)}
	}
	model := NewValidatorLifecycleModel(ValidatorModelOptions{
		NodeCount:     nodeCount,
		InitialActive: nodeCount,
		Behavior:      behavior,
	})

	for step, op := range program {
		node := model.Order[programNodeIndex(program, step)%len(model.Order)]
		action := ModelAction{
			Kind: ModelActionKind(op % byte(modelActionCount)),
			Node: node,
		}
		if err := model.Apply(step, action); err != nil {
			return ModelResult{
				NodeCount: nodeCount,
				Steps:     step + 1,
				Height:    model.Height,
				Events:    model.Events,
			}, err
		}
	}

	return ModelResult{
		NodeCount: nodeCount,
		Steps:     len(program),
		Height:    model.Height,
		Events:    model.Events,
	}, nil
}

func programNodeIndex(program []byte, offset int) int {
	if len(program) == 0 {
		return 0
	}
	hi := int(program[offset%len(program)])
	lo := int(program[(offset+1)%len(program)])
	return hi<<8 | lo
}

func IsModelInvariantError(err error) bool {
	var invariantErr *ModelInvariantError
	return errors.As(err, &invariantErr)
}

func randomModelAction(rng *rand.Rand, nodes []NodeID) ModelAction {
	node := nodes[rng.Intn(len(nodes))]
	roll := rng.Intn(100)
	switch {
	case roll < 16:
		return ModelAction{Kind: ModelTick, Node: node}
	case roll < 28:
		return ModelAction{Kind: ModelStop, Node: node}
	case roll < 40:
		return ModelAction{Kind: ModelStart, Node: node}
	case roll < 52:
		return ModelAction{Kind: ModelJail, Node: node}
	case roll < 64:
		return ModelAction{Kind: ModelDeregister, Node: node}
	case roll < 72:
		return ModelAction{Kind: ModelDeregisterTwice, Node: node}
	case roll < 82:
		return ModelAction{Kind: ModelRegister, Node: node}
	case roll < 88:
		return ModelAction{Kind: ModelUnjail, Node: node}
	case roll < 94:
		return ModelAction{Kind: ModelLieEndpoint, Node: node}
	default:
		return ModelAction{Kind: ModelRepairEndpoint, Node: node}
	}
}

func clamp(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
