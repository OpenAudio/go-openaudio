package entity_manager

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestDispatcher_PostHook_FiresAfterSuccessfulHandle(t *testing.T) {
	d := NewDispatcher(nil)
	var handlerCalled, hookCalled bool
	d.Register(&stubHandler{
		entityType: EntityTypeUser,
		action:     ActionCreate,
		fn: func(_ context.Context, _ *Params) error {
			handlerCalled = true
			return nil
		},
	})
	d.RegisterPostHook(EntityTypeUser, ActionCreate, func(_ context.Context, p *Params) error {
		hookCalled = true
		if p.EntityType != EntityTypeUser || p.Action != ActionCreate {
			t.Errorf("hook saw wrong params: %s/%s", p.EntityType, p.Action)
		}
		return nil
	})

	params := &Params{EntityType: EntityTypeUser, Action: ActionCreate, EntityID: 42}
	if err := d.Dispatch(context.Background(), params); err != nil {
		t.Fatalf("Dispatch error: %v", err)
	}
	if !handlerCalled {
		t.Error("handler was not called")
	}
	if !hookCalled {
		t.Error("post-hook was not called")
	}
}

func TestDispatcher_PostHook_SkippedOnHandlerError(t *testing.T) {
	d := NewDispatcher(nil)
	hookCalled := false
	d.Register(&stubHandler{
		entityType: EntityTypeUser,
		action:     ActionCreate,
		fn: func(_ context.Context, _ *Params) error {
			return NewValidationError("user already exists")
		},
	})
	d.RegisterPostHook(EntityTypeUser, ActionCreate, func(_ context.Context, _ *Params) error {
		hookCalled = true
		return nil
	})

	params := &Params{EntityType: EntityTypeUser, Action: ActionCreate}
	err := d.Dispatch(context.Background(), params)
	if err == nil || !IsValidationError(err) {
		t.Fatalf("expected ValidationError, got %v", err)
	}
	if hookCalled {
		t.Error("post-hook must not run when handler returned an error")
	}
}

func TestDispatcher_PostHook_ErrorIsLoggedButSwallowed(t *testing.T) {
	core, observed := observer.New(zap.WarnLevel)
	logger := zap.New(core)
	d := NewDispatcher(logger)

	d.Register(&stubHandler{
		entityType: EntityTypeUser,
		action:     ActionCreate,
		fn:         func(_ context.Context, _ *Params) error { return nil },
	})
	d.RegisterPostHook(EntityTypeUser, ActionCreate, func(_ context.Context, _ *Params) error {
		return errors.New("hook blew up")
	})

	params := &Params{EntityType: EntityTypeUser, Action: ActionCreate, EntityID: 7}
	if err := d.Dispatch(context.Background(), params); err != nil {
		t.Fatalf("Dispatch should not propagate hook errors, got %v", err)
	}
	if observed.FilterMessage("post-hook returned error").Len() != 1 {
		t.Errorf("expected one warn log, got %d entries: %+v", observed.Len(), observed.All())
	}
}

func TestDispatcher_PostHook_MultipleRunInRegistrationOrder(t *testing.T) {
	d := NewDispatcher(nil)
	d.Register(&stubHandler{
		entityType: EntityTypeTrack,
		action:     ActionCreate,
		fn:         func(_ context.Context, _ *Params) error { return nil },
	})
	var order []string
	d.RegisterPostHook(EntityTypeTrack, ActionCreate, func(_ context.Context, _ *Params) error {
		order = append(order, "first")
		return nil
	})
	d.RegisterPostHook(EntityTypeTrack, ActionCreate, func(_ context.Context, _ *Params) error {
		order = append(order, "second")
		return nil
	})
	// Second hook errors; third still runs.
	d.RegisterPostHook(EntityTypeTrack, ActionCreate, func(_ context.Context, _ *Params) error {
		order = append(order, "third")
		return errors.New("explode")
	})
	d.RegisterPostHook(EntityTypeTrack, ActionCreate, func(_ context.Context, _ *Params) error {
		order = append(order, "fourth")
		return nil
	})

	params := &Params{EntityType: EntityTypeTrack, Action: ActionCreate}
	if err := d.Dispatch(context.Background(), params); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	want := []string{"first", "second", "third", "fourth"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i, v := range want {
		if order[i] != v {
			t.Errorf("[%d] = %q, want %q", i, order[i], v)
		}
	}
}

func TestDispatcher_PostHook_DoesNotFireForOtherActions(t *testing.T) {
	d := NewDispatcher(nil)
	d.Register(&stubHandler{
		entityType: EntityTypeUser,
		action:     ActionCreate,
		fn:         func(_ context.Context, _ *Params) error { return nil },
	})
	d.Register(&stubHandler{
		entityType: EntityTypeUser,
		action:     ActionUpdate,
		fn:         func(_ context.Context, _ *Params) error { return nil },
	})

	createHookCalls := 0
	d.RegisterPostHook(EntityTypeUser, ActionCreate, func(_ context.Context, _ *Params) error {
		createHookCalls++
		return nil
	})

	// Create — hook fires.
	if err := d.Dispatch(context.Background(), &Params{EntityType: EntityTypeUser, Action: ActionCreate}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Update — same entity, different action — hook must NOT fire.
	if err := d.Dispatch(context.Background(), &Params{EntityType: EntityTypeUser, Action: ActionUpdate}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if createHookCalls != 1 {
		t.Errorf("create hook fired %d times, want 1", createHookCalls)
	}
}

func TestDispatcher_PostHook_NoHandlerNoHook(t *testing.T) {
	d := NewDispatcher(nil)
	hookCalled := false
	d.RegisterPostHook(EntityTypeUser, ActionCreate, func(_ context.Context, _ *Params) error {
		hookCalled = true
		return nil
	})

	// No handler registered → Dispatch reports ErrNoHandler and no hook runs.
	// A post-hook must never stand in for the handler it was attached to.
	err := d.Dispatch(context.Background(), &Params{EntityType: EntityTypeUser, Action: ActionCreate})
	if !errors.Is(err, ErrNoHandler) {
		t.Fatalf("Dispatch: got %v, want ErrNoHandler", err)
	}
	if hookCalled {
		t.Error("hook ran for an entity/action with no registered handler")
	}
}
