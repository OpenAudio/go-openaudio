package entity_manager

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// An unroutable transaction must be reported, not silently accepted. Reporting
// it as success is what let 73,201 comment reactions vanish without a single
// rejection being counted. Callers decide whether to tolerate it; they cannot
// decide anything if Dispatch does not tell them.
func TestDispatcher_NoHandler(t *testing.T) {
	d := NewDispatcher(nil)
	params := &Params{EntityType: EntityTypeUser, Action: ActionCreate}
	err := d.Dispatch(context.Background(), params)
	if !errors.Is(err, ErrNoHandler) {
		t.Fatalf("Dispatch with no handler: got %v, want ErrNoHandler", err)
	}
	// The message has to name the pair, or a run reports a count with no way
	// to tell which entity type went missing.
	if !strings.Contains(err.Error(), "User/Create") {
		t.Errorf("error %q does not identify the unrouted entity_type/action", err)
	}
}

// A handler registered against the wildcard entity type still routes, so the
// social handlers are not caught by the unrouted path.
func TestDispatcher_NoHandler_WildcardStillRoutes(t *testing.T) {
	d := NewDispatcher(nil)
	called := false
	d.Register(&stubHandler{
		entityType: EntityTypeAny,
		action:     ActionFollow,
		fn:         func(context.Context, *Params) error { called = true; return nil },
	})
	if err := d.Dispatch(context.Background(), &Params{EntityType: EntityTypeUser, Action: ActionFollow}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !called {
		t.Error("wildcard handler did not run")
	}
}

func TestDispatcher_RegisterAndDispatch(t *testing.T) {
	d := NewDispatcher(nil)
	called := false
	d.Register(&stubHandler{
		entityType: EntityTypeUser,
		action:     ActionCreate,
		fn:         func(_ context.Context, _ *Params) error { called = true; return nil },
	})

	if !d.HasHandler(EntityTypeUser, ActionCreate) {
		t.Fatal("expected handler to be registered")
	}
	if d.HasHandler(EntityTypeTrack, ActionCreate) {
		t.Fatal("expected no handler for Track:Create")
	}
	if d.HandlerCount() != 1 {
		t.Fatalf("expected 1 handler, got %d", d.HandlerCount())
	}

	params := &Params{EntityType: EntityTypeUser, Action: ActionCreate}
	if err := d.Dispatch(context.Background(), params); err != nil {
		t.Fatalf("Dispatch error: %v", err)
	}
	if !called {
		t.Fatal("handler was not called")
	}
}

func TestDispatcher_ValidationError(t *testing.T) {
	d := NewDispatcher(nil)
	d.Register(&stubHandler{
		entityType: EntityTypeUser,
		action:     ActionCreate,
		fn: func(_ context.Context, _ *Params) error {
			return NewValidationError("user already exists")
		},
	})

	params := &Params{EntityType: EntityTypeUser, Action: ActionCreate}
	err := d.Dispatch(context.Background(), params)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !IsValidationError(err) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
}

func TestValidationError(t *testing.T) {
	err := NewValidationError("user %d already exists", 42)
	if err.Error() != "user 42 already exists" {
		t.Fatalf("unexpected message: %s", err.Error())
	}
	if !IsValidationError(err) {
		t.Fatal("IsValidationError should return true")
	}
}

// stubHandler is a test-only Handler implementation.
type stubHandler struct {
	entityType string
	action     string
	fn         func(context.Context, *Params) error
}

func (s *stubHandler) EntityType() string { return s.entityType }
func (s *stubHandler) Action() string     { return s.action }
func (s *stubHandler) Handle(ctx context.Context, params *Params) error {
	return s.fn(ctx, params)
}
