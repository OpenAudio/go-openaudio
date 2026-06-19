package fuzz

import (
	"errors"
	"testing"
)

func TestValidatorLifecycleModelCatchesJailedDeregistrationBug(t *testing.T) {
	t.Run("current behavior skips comet update for jailed deregistration", func(t *testing.T) {
		model := NewValidatorLifecycleModel(ValidatorModelOptions{
			NodeCount:     1,
			InitialActive: 1,
			Behavior:      ValidatorSetBehaviorCurrent,
		})

		if err := model.Apply(0, ModelAction{Kind: ModelJail, Node: "node1"}); err != nil {
			t.Fatal(err)
		}
		if err := model.Apply(1, ModelAction{Kind: ModelDeregister, Node: "node1"}); err != nil {
			t.Fatal(err)
		}

		last := model.Events[len(model.Events)-1]
		if last.EmittedUpdate {
			t.Fatalf("jailed deregistration emitted a comet update: %+v", last)
		}
	})

	t.Run("buggy behavior emits invalid update for jailed deregistration", func(t *testing.T) {
		model := NewValidatorLifecycleModel(ValidatorModelOptions{
			NodeCount:     1,
			InitialActive: 1,
			Behavior:      ValidatorSetBehaviorBuggyJailedDeregistration,
		})

		if err := model.Apply(0, ModelAction{Kind: ModelJail, Node: "node1"}); err != nil {
			t.Fatal(err)
		}
		err := model.Apply(1, ModelAction{Kind: ModelDeregister, Node: "node1"})
		if !IsModelInvariantError(err) {
			t.Fatalf("expected invariant error, got %v", err)
		}
	})
}

func TestValidatorLifecycleModelDuplicateDeregisterDoesNotEmitTwice(t *testing.T) {
	model := NewValidatorLifecycleModel(ValidatorModelOptions{
		NodeCount:     1,
		InitialActive: 1,
		Behavior:      ValidatorSetBehaviorCurrent,
	})

	if err := model.Apply(0, ModelAction{Kind: ModelDeregisterTwice, Node: "node1"}); err != nil {
		t.Fatal(err)
	}
	if len(model.Events) != 2 {
		t.Fatalf("expected two deregistration events, got %d", len(model.Events))
	}
	if !model.Events[0].EmittedUpdate {
		t.Fatalf("first active deregistration did not emit comet update: %+v", model.Events[0])
	}
	if model.Events[1].EmittedUpdate {
		t.Fatalf("duplicate deregistration emitted comet update: %+v", model.Events[1])
	}
}

func TestValidatorLifecycleModelStress300Nodes(t *testing.T) {
	for seed := int64(1); seed <= 100; seed++ {
		result, err := RunValidatorLifecycleModel(seed, DefaultModelNodeLimit, 10_000, ValidatorSetBehaviorCurrent)
		if err != nil {
			t.Fatalf("seed=%d node_count=%d steps=%d height=%d err=%v", seed, result.NodeCount, result.Steps, result.Height, err)
		}
	}
}

func TestValidatorLifecycleProgramClampsAt300Nodes(t *testing.T) {
	result, err := RunValidatorLifecycleProgram([]byte{byte(ModelTick)}, 10_000, ValidatorSetBehaviorCurrent)
	if err != nil {
		t.Fatal(err)
	}
	if result.NodeCount != DefaultModelNodeLimit {
		t.Fatalf("expected node count to clamp at %d, got %d", DefaultModelNodeLimit, result.NodeCount)
	}
}

func TestValidatorLifecycleProgramCanAddressNode300(t *testing.T) {
	index := programNodeIndex([]byte{1, 43}, 0)
	if index%DefaultModelNodeLimit != 299 {
		t.Fatalf("expected node index 299, got %d", index%DefaultModelNodeLimit)
	}
}

func TestValidatorLifecycleProgramBuggyBehaviorFindsSeed(t *testing.T) {
	_, err := RunValidatorLifecycleProgram([]byte{
		byte(ModelJail),
		byte(ModelDeregister),
	}, 1, ValidatorSetBehaviorBuggyJailedDeregistration)
	var invariantErr *ModelInvariantError
	if !errors.As(err, &invariantErr) {
		t.Fatalf("expected invariant error, got %v", err)
	}
}

func TestValidatorLifecycleModelCatchesAdditionalBugClasses(t *testing.T) {
	tests := []struct {
		name     string
		behavior ValidatorSetBehavior
		actions  []ModelAction
	}{
		{
			name:     "duplicate deregistration emits repeated update",
			behavior: ValidatorSetBehaviorBuggyAnyDeregistrationUpdate,
			actions: []ModelAction{
				{Kind: ModelDeregisterTwice, Node: "node1"},
			},
		},
		{
			name:     "registration updates app state without comet set",
			behavior: ValidatorSetBehaviorBuggyRegisterWithoutCometUpdate,
			actions: []ModelAction{
				{Kind: ModelDeregister, Node: "node1"},
				{Kind: ModelRegister, Node: "node1"},
			},
		},
		{
			name:     "absent validator marked online",
			behavior: ValidatorSetBehaviorBuggyStartAbsentOnline,
			actions: []ModelAction{
				{Kind: ModelDeregister, Node: "node1"},
				{Kind: ModelStart, Node: "node1"},
			},
		},
		{
			name:     "height advances without quorum",
			behavior: ValidatorSetBehaviorBuggyTickWithoutQuorum,
			actions: []ModelAction{
				{Kind: ModelStop, Node: "node1"},
				{Kind: ModelStop, Node: "node2"},
				{Kind: ModelTick, Node: "node3"},
			},
		},
		{
			name:     "height stalls with quorum",
			behavior: ValidatorSetBehaviorBuggyStallWithQuorum,
			actions: []ModelAction{
				{Kind: ModelTick, Node: "node1"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := NewValidatorLifecycleModel(ValidatorModelOptions{
				NodeCount:     3,
				InitialActive: 3,
				Behavior:      test.behavior,
			})
			var err error
			for i, action := range test.actions {
				err = model.Apply(i, action)
				if err != nil {
					break
				}
			}
			if !IsModelInvariantError(err) {
				t.Fatalf("expected invariant error, got %v", err)
			}
		})
	}
}
