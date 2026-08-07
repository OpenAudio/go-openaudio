package entity_manager

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// stubRow satisfies pgx.Row for the EXISTS(...) lookups, always reporting
// "not found" so the stateful checks resolve without a live database.
type stubRow struct{}

func (stubRow) Scan(dest ...any) error {
	for _, d := range dest {
		switch v := d.(type) {
		case *bool:
			*v = false
		case *string:
			*v = ""
		}
	}
	return nil
}

// stubDBTX records the arguments of the INSERT so a test can assert which
// account-state flags were written.
type stubDBTX struct {
	execArgs []any
}

func (s *stubDBTX) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	s.execArgs = args
	return pgconn.CommandTag{}, nil
}
func (s *stubDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (s *stubDBTX) QueryRow(context.Context, string, ...any) pgx.Row        { return stubRow{} }

func legacyUserParams(t *testing.T, dbtx *stubDBTX, metadata string) *Params {
	t.Helper()
	p := &Params{
		UserID:      999, // legacy id, below UserIDOffset
		EntityType:  EntityTypeUser,
		Action:      ActionCreate,
		Signer:      "0xabc123",
		RawMetadata: metadata,
		DBTX:        dbtx,
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
		t.Fatalf("bad metadata fixture: %v", err)
	}
	p.Metadata = meta
	return p
}

// A migrated user keeps its legacy id and may carry a now-reserved handle and an
// over-long bio/name. The migration handler must accept it: rejecting these rows
// would silently drop real accounts and make the indexed state diverge from the
// source dump.
func TestMigratedUserCreate_AcceptsLegacyRow(t *testing.T) {
	metadata := `{"handle":"admin","name":"` + strings.Repeat("x", CharacterLimitUserName+1) +
		`","bio":"` + strings.Repeat("y", CharacterLimitUserBio+1) + `"}`

	dbtx := &stubDBTX{}
	if err := migratedUserCreate().Handle(context.Background(), legacyUserParams(t, dbtx, metadata)); err != nil {
		t.Fatalf("migrated legacy user should be accepted, got: %v", err)
	}
	if dbtx.execArgs == nil {
		t.Fatal("expected an INSERT to be issued")
	}
}

// The production handler must still reject that same row, so the relaxed policy
// is reachable only through the migration handler set.
func TestProductionUserCreate_StillRejectsLegacyRow(t *testing.T) {
	metadata := `{"handle":"admin","name":"Alice","bio":"hello"}`

	err := UserCreate().Handle(context.Background(), legacyUserParams(t, &stubDBTX{}, metadata))
	if err == nil {
		t.Fatal("production handler should reject a below-offset user, got nil")
	}
	if !IsValidationError(err) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "below offset") {
		t.Fatalf("expected the id-offset error, got %q", err.Error())
	}
}

// Account state must survive the round trip from source metadata into the INSERT,
// otherwise verified users land unverified and deactivated users land active.
func TestMigratedUserCreate_PipesAccountState(t *testing.T) {
	tests := []struct {
		name                                     string
		metadata                                 string
		wantVerified, wantDeactivated, wantAvail bool
	}{
		{"verified", `{"handle":"a","is_verified":true,"is_deactivated":false,"is_available":true}`, true, false, true},
		{"deactivated", `{"handle":"b","is_verified":false,"is_deactivated":true,"is_available":true}`, false, true, true},
		{"unavailable", `{"handle":"c","is_verified":false,"is_deactivated":false,"is_available":false}`, false, false, false},
		// Absent flags fall back to new-account defaults.
		{"absent", `{"handle":"d"}`, false, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbtx := &stubDBTX{}
			if err := migratedUserCreate().Handle(context.Background(), legacyUserParams(t, dbtx, tt.metadata)); err != nil {
				t.Fatalf("handle: %v", err)
			}
			// The three state flags are the trailing INSERT arguments.
			n := len(dbtx.execArgs)
			if n < 3 {
				t.Fatalf("expected at least 3 INSERT args, got %d", n)
			}
			got := [3]any{dbtx.execArgs[n-3], dbtx.execArgs[n-2], dbtx.execArgs[n-1]}
			want := [3]any{tt.wantVerified, tt.wantDeactivated, tt.wantAvail}
			if got != want {
				t.Errorf("state flags = %v, want %v (is_verified, is_deactivated, is_available)", got, want)
			}
		})
	}
}

// A new account must never be able to self-assign verification through metadata:
// the production insert ignores the flags entirely.
func TestProductionInsertUser_IgnoresStateMetadata(t *testing.T) {
	dbtx := &stubDBTX{}
	params := legacyUserParams(t, dbtx, `{"handle":"e","is_verified":true,"is_deactivated":true,"is_available":false}`)

	if err := insertUser(context.Background(), params); err != nil {
		t.Fatalf("insertUser: %v", err)
	}
	n := len(dbtx.execArgs)
	got := [3]any{dbtx.execArgs[n-3], dbtx.execArgs[n-2], dbtx.execArgs[n-1]}
	want := [3]any{false, false, true}
	if got != want {
		t.Errorf("new-account flags = %v, want %v — metadata must not be trusted here", got, want)
	}
}

// A soft-deleted social row must replay as a single transaction carrying
// is_delete, rather than being dropped or needing a create/delete pair.
func TestMigratedSocial_PipesIsDelete(t *testing.T) {
	for _, isDelete := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "deleted"}[isDelete], func(t *testing.T) {
			var gotIsDelete bool
			h := migratedSocial(EntityTypeAny, ActionFollow,
				func(context.Context, *Params) error { return nil },
				func(_ context.Context, _ *Params, d bool) error { gotIsDelete = d; return nil },
			)

			meta := `{"created_at":"2026-01-01T00:00:00Z","is_delete":false}`
			if isDelete {
				meta = `{"created_at":"2026-01-01T00:00:00Z","is_delete":true}`
			}
			if err := h.Handle(context.Background(), legacyUserParams(t, &stubDBTX{}, meta)); err != nil {
				t.Fatalf("handle: %v", err)
			}
			if gotIsDelete != isDelete {
				t.Errorf("is_delete passed to insert = %v, want %v", gotIsDelete, isDelete)
			}
		})
	}
}

// A failing validator must stop the write.
func TestMigratedSocial_ValidationBlocksWrite(t *testing.T) {
	wrote := false
	h := migratedSocial(EntityTypeAny, ActionFollow,
		func(context.Context, *Params) error { return NewValidationError("nope") },
		func(context.Context, *Params, bool) error { wrote = true; return nil },
	)

	if err := h.Handle(context.Background(), legacyUserParams(t, &stubDBTX{}, `{}`)); err == nil {
		t.Fatal("expected the validation error to propagate")
	}
	if wrote {
		t.Error("insert ran despite failed validation")
	}
}

// The migration set must differ from production only where intended.
func TestRegisterMigrationOverrides_ReplacesOnlyIntendedHandlers(t *testing.T) {
	prod := NewDispatcher(nil)
	prod.Register(UserCreate())
	prod.Register(UserUpdate()) // not overridden — the control
	prod.Register(TrackCreate())

	mig := prod.Clone()
	RegisterMigrationOverrides(mig)

	// Overridden: creates whose validation policy differs for replayed state.
	for _, k := range []string{
		handlerKey(EntityTypeUser, ActionCreate),
		handlerKey(EntityTypeTrack, ActionCreate),
	} {
		if mig.handlers[k] == prod.handlers[k] {
			t.Errorf("%s should be overridden in the migration set", k)
		}
	}

	// Untouched: everything else keeps production behavior.
	if k := handlerKey(EntityTypeUser, ActionUpdate); mig.handlers[k] != prod.handlers[k] {
		t.Errorf("%s should still be the production handler", k)
	}
	// Cloning must not mutate the production set.
	if _, ok := prod.handlers[handlerKey(EntityTypeUser, ActionCreate)].(*userCreateHandler); !ok {
		t.Error("production dispatcher was mutated by the migration overrides")
	}
}

// A handful of legacy tracks have an empty title. Production still requires one
// — the check belongs to validation, not to writing the row — while the
// migration handler, which does not run that validator, keeps the track.
func TestTitleRequiredOnlyForNewTracks(t *testing.T) {
	params := &Params{
		UserID:     1,
		EntityID:   TrackIDOffset + 1, // above the offset so validation reaches the title check
		EntityType: EntityTypeTrack,
		Action:     ActionCreate,
		Signer:     "0xabc123",
		DBTX:       &stubDBTX{},
		Metadata:   map[string]any{"owner_id": float64(1)},
	}

	err := validateTrackCreate(context.Background(), params)
	if err == nil || !strings.Contains(err.Error(), "title is required") {
		t.Fatalf("production create should reject an empty title, got %v", err)
	}
}

// aggregate_plays is owned by the consumer, so it is absent from some ETL
// databases. A Reconcile there must be skipped, not fail: an undefined relation
// is not a ValidationError and would abort the whole block.
func TestPlayCountReconcile_SkippedWhenTableAbsent(t *testing.T) {
	dbtx := &stubDBTX{} // to_regclass scans as NULL -> absent
	h := PlayCountReconcile()
	params := &Params{
		EntityID: 42,
		DBTX:     dbtx,
		Metadata: map[string]any{"delta": float64(7)},
	}

	if err := h.Handle(context.Background(), params); err != nil {
		t.Fatalf("Reconcile must be skipped when aggregate_plays is absent, got: %v", err)
	}
	if dbtx.execArgs != nil {
		t.Error("no INSERT should have been attempted against the missing table")
	}
}

// A malformed Reconcile is still a validation error, not a silent skip.
func TestPlayCountReconcile_RequiresDelta(t *testing.T) {
	err := PlayCountReconcile().Handle(context.Background(), &Params{
		EntityID: 42,
		DBTX:     &stubDBTX{},
		Metadata: map[string]any{},
	})
	if err == nil || !IsValidationError(err) {
		t.Fatalf("expected a ValidationError for a missing delta, got %v", err)
	}
}

// The migration create must carry the profile fields the live protocol only
// ever sends on Update: a migrated Create represents an account's final state,
// not a signup. The production path must keep ignoring them.
func TestMigratedUserCreate_CarriesUpdateOnlyProfileFields(t *testing.T) {
	meta := `{"handle":"e","playlist_library":{"contents":[{"playlist_id":7}]},` +
		`"artist_pick_track_id":42,"allow_ai_attribution":true,"profile_type":"label",` +
		`"spl_usdc_payout_wallet":"SoLWaLLeT","coin_flair_mint":"MiNt111"}`

	t.Run("migration carries every field", func(t *testing.T) {
		dbtx := &stubDBTX{}
		params := legacyUserParams(t, dbtx, meta)
		if err := (&migratedUserCreateHandler{}).Handle(context.Background(), params); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		lib, _ := insertedUserValue(t, dbtx.execArgs, "playlist_library").([]byte)
		if !bytes.Contains(lib, []byte(`"playlist_id":7`)) {
			t.Errorf("playlist_library = %s, want the source library", lib)
		}
		if pick, ok := insertedUserValue(t, dbtx.execArgs, "artist_pick_track_id").(*int64); !ok || pick == nil || *pick != 42 {
			t.Errorf("artist_pick_track_id = %v, want 42", insertedUserValue(t, dbtx.execArgs, "artist_pick_track_id"))
		}
		if v := insertedUserValue(t, dbtx.execArgs, "allow_ai_attribution"); v != true {
			t.Errorf("allow_ai_attribution = %v, want true", v)
		}
		if v := insertedUserValue(t, dbtx.execArgs, "profile_type"); v != "label" {
			t.Errorf("profile_type = %v, want label", v)
		}
	})

	// artist_pick_track_id is the one field a live Create must not set: it
	// references a track the account cannot own yet. Everything else in the
	// metadata is part of the API's create contract and must persist.
	t.Run("production sets everything except artist_pick_track_id", func(t *testing.T) {
		dbtx := &stubDBTX{}
		params := legacyUserParams(t, dbtx, meta)
		if err := insertUser(context.Background(), params); err != nil {
			t.Fatalf("insertUser: %v", err)
		}
		if v := insertedUserValue(t, dbtx.execArgs, "artist_pick_track_id"); v != (*int64)(nil) {
			t.Errorf("artist_pick_track_id = %v, want nil on a live create", v)
		}
		for col, want := range map[string]any{
			"allow_ai_attribution":   true,
			"profile_type":           "label",
			"spl_usdc_payout_wallet": "SoLWaLLeT",
			"coin_flair_mint":        "MiNt111",
		} {
			if v := insertedUserValue(t, dbtx.execArgs, col); v != want {
				t.Errorf("%s = %v, want %v -- the API create body accepts it", col, v, want)
			}
		}
		if lib, _ := insertedUserValue(t, dbtx.execArgs, "playlist_library").([]byte); !bytes.Contains(lib, []byte(`"playlist_id":7`)) {
			t.Errorf("playlist_library = %s, want it persisted on a live create", lib)
		}
	})
}

func TestMigratedCreateHandlersCarrySoftDeleteState(t *testing.T) {
	if h := (&migratedCommentCreateHandler{}); h.Action() != ActionCreate || h.EntityType() != EntityTypeComment {
		t.Errorf("comment override = %s/%s", h.EntityType(), h.Action())
	}
	if h := (&migratedDeveloperAppCreateHandler{}); h.Action() != ActionCreate || h.EntityType() != EntityTypeDeveloperApp {
		t.Errorf("developer app override = %s/%s", h.EntityType(), h.Action())
	}
	if h := (&migratedGrantCreateHandler{}); h.Action() != ActionCreate || h.EntityType() != EntityTypeGrant {
		t.Errorf("grant override = %s/%s", h.EntityType(), h.Action())
	}

	// The overrides must be registered, or the production handlers run and the
	// state is silently hardcoded back to "active".
	d := NewDispatcher(nil)
	RegisterMigrationOverrides(d)
	for _, tt := range []struct{ entity, action string }{
		{EntityTypeComment, ActionCreate},
		{EntityTypeDeveloperApp, ActionCreate},
		{EntityTypeGrant, ActionCreate},
	} {
		if !d.HasHandler(tt.entity, tt.action) {
			t.Errorf("no handler registered for %s/%s", tt.entity, tt.action)
		}
	}
}

// An unlinked wallet migrates as a single Create carrying is_delete, so the
// production insert must keep defaulting to "linked" while the migration
// override honours the source row's state.
func TestMigratedWalletCreateCarriesIsDelete(t *testing.T) {
	for _, tt := range []struct {
		name string
		meta map[string]any
		want bool
	}{
		{"active link", map[string]any{}, false},
		{"unlinked", map[string]any{"is_delete": true}, true},
		{"explicitly active", map[string]any{"is_delete": false}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := (&Params{Metadata: tt.meta}).MetadataBoolOr("is_delete", false)
			if got != tt.want {
				t.Errorf("is_delete = %v, want %v", got, tt.want)
			}
		})
	}

	d := NewDispatcher(nil)
	RegisterMigrationOverrides(d)
	for _, e := range []string{EntityTypeAssociatedWallet, EntityTypeDashboardWalletUser} {
		if !d.HasHandler(e, ActionCreate) {
			t.Errorf("no migration override registered for %s/Create", e)
		}
	}
}

// A migrated track keeps the slug it already serves. Regenerating it would
// silently change 618,066 permalinks on a production clone.
func TestMigratedTrackRoute(t *testing.T) {
	for _, tt := range []struct {
		name string
		meta map[string]any
		want *trackRoute
	}{
		{"carried verbatim",
			map[string]any{"route_slug": "guava-rain-2", "route_title_slug": "guava-rain-2", "route_collision_id": float64(0)},
			&trackRoute{Slug: "guava-rain-2", TitleSlug: "guava-rain-2"}},
		{"random slug for an untitled track",
			map[string]any{"route_slug": "k2rX2M3"},
			&trackRoute{Slug: "k2rX2M3", TitleSlug: "k2rX2M3"}},
		{"collision id preserved",
			map[string]any{"route_slug": "test-1", "route_title_slug": "test", "route_collision_id": float64(1)},
			&trackRoute{Slug: "test-1", TitleSlug: "test", CollisionID: 1}},
		{"absent falls back to generation", map[string]any{}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := migratedTrackRoute(&Params{Metadata: tt.meta})
			switch {
			case tt.want == nil && got != nil:
				t.Fatalf("got %+v, want nil so the slug is generated", got)
			case tt.want != nil && got == nil:
				t.Fatal("got nil, want the carried route")
			case tt.want != nil && *got != *tt.want:
				t.Errorf("got %+v, want %+v", *got, *tt.want)
			}
		})
	}
}
