package entity_manager

import (
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
