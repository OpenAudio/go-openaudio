package etl

import (
	"context"
	"testing"

	em "github.com/OpenAudio/go-openaudio/pkg/etl/processors/entity_manager"
)

// TestSetCreatedHooks_StageOntoDispatcher verifies that the Set*CreatedHook
// convenience methods stage hooks for the correct (entity_type, action)
// keys, and that applyPendingPostHooks transfers them onto the dispatcher.
// This is the contract API consumers (api/) depend on; the dispatcher-side
// behavior is covered by tests in pkg/etl/processors/entity_manager.
func TestSetCreatedHooks_StageOntoDispatcher(t *testing.T) {
	e := &Indexer{}

	userFired := false
	trackFired := false
	playlistFired := false

	e.SetUserCreatedHook(func(_ context.Context, _ *em.Params) error {
		userFired = true
		return nil
	})
	e.SetTrackCreatedHook(func(_ context.Context, _ *em.Params) error {
		trackFired = true
		return nil
	})
	e.SetPlaylistCreatedHook(func(_ context.Context, _ *em.Params) error {
		playlistFired = true
		return nil
	})

	if got := len(e.pendingPostHooks); got != 3 {
		t.Fatalf("expected 3 pending hooks, got %d", got)
	}

	// Simulate the relevant chunk of Run(): build a dispatcher, register
	// handlers, then apply the pending hooks. We use stub handlers so we
	// don't need a real DB.
	e.dispatcher = em.NewDispatcher(nil)
	e.dispatcher.Register(stubHandlerFn(em.EntityTypeUser, em.ActionCreate))
	e.dispatcher.Register(stubHandlerFn(em.EntityTypeTrack, em.ActionCreate))
	e.dispatcher.Register(stubHandlerFn(em.EntityTypePlaylist, em.ActionCreate))
	e.applyPendingPostHooks()

	// Dispatch each — its hook should fire and the others must not.
	dispatch := func(entityType, action string) {
		t.Helper()
		if err := e.dispatcher.Dispatch(context.Background(), &em.Params{
			EntityType: entityType,
			Action:     action,
		}); err != nil {
			t.Fatalf("dispatch %s/%s: %v", entityType, action, err)
		}
	}

	dispatch(em.EntityTypeUser, em.ActionCreate)
	if !userFired {
		t.Error("user hook did not fire on User/Create")
	}
	if trackFired || playlistFired {
		t.Error("non-user hooks fired on User/Create")
	}

	userFired = false
	dispatch(em.EntityTypeTrack, em.ActionCreate)
	if !trackFired {
		t.Error("track hook did not fire on Track/Create")
	}
	if userFired || playlistFired {
		t.Error("non-track hooks fired on Track/Create")
	}

	trackFired = false
	dispatch(em.EntityTypePlaylist, em.ActionCreate)
	if !playlistFired {
		t.Error("playlist hook did not fire on Playlist/Create")
	}
	if userFired || trackFired {
		t.Error("non-playlist hooks fired on Playlist/Create")
	}
}

func TestRegisterPostHook_GenericPath(t *testing.T) {
	e := &Indexer{}
	called := false
	// Use the generic API directly — e.g. for an action without typed
	// sugar (like ActionUpdate).
	e.RegisterPostHook(em.EntityTypeUser, em.ActionUpdate, func(_ context.Context, _ *em.Params) error {
		called = true
		return nil
	})

	e.dispatcher = em.NewDispatcher(nil)
	e.dispatcher.Register(stubHandlerFn(em.EntityTypeUser, em.ActionUpdate))
	e.applyPendingPostHooks()

	if err := e.dispatcher.Dispatch(context.Background(), &em.Params{
		EntityType: em.EntityTypeUser,
		Action:     em.ActionUpdate,
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !called {
		t.Error("RegisterPostHook hook did not fire")
	}
}

// stubHandlerFn returns a no-op Handler for the given (entity_type, action).
type stubHandler struct {
	entityType, action string
}

func (s *stubHandler) EntityType() string                                 { return s.entityType }
func (s *stubHandler) Action() string                                     { return s.action }
func (s *stubHandler) Handle(_ context.Context, _ *em.Params) error       { return nil }
func stubHandlerFn(entityType, action string) *stubHandler                { return &stubHandler{entityType, action} }
