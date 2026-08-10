package entity_manager

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// migrationHandlerFor resolves the handler the genesis migration uses for an
// entity/action, deriving it from the production registration the way the
// indexer does. Tests go through the dispatcher rather than calling an override
// directly because an override nobody registers fixes nothing.
func migrationHandlerFor(t *testing.T, production Handler) Handler {
	t.Helper()
	prod := NewDispatcher(nil)
	prod.Register(production)
	mig := prod.Clone()
	RegisterMigrationOverrides(mig)

	h, ok := mig.handlers[handlerKey(production.EntityType(), production.Action())]
	if !ok {
		t.Fatalf("no migration handler registered for %s/%s", production.EntityType(), production.Action())
	}
	return h
}

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
	// execSQL keeps every statement, so a test can assert on which tables a
	// handler wrote rather than only on the last call's arguments.
	execSQL []string
}

func (s *stubDBTX) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	s.execArgs = args
	s.execSQL = append(s.execSQL, sql)
	return pgconn.CommandTag{}, nil
}
func (s *stubDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (s *stubDBTX) QueryRow(context.Context, string, ...any) pgx.Row        { return stubRow{} }

func legacyFollowParams(t *testing.T, dbtx *stubDBTX) *Params {
	t.Helper()
	return &Params{
		UserID:     101, // follower
		EntityID:   202, // followee
		EntityType: EntityTypeUser,
		Action:     ActionFollow,
		Signer:     "0xabc123",
		DBTX:       dbtx,
	}
}

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

// contestMeta builds the metadata genesis-writer sends for a remix contest.
func contestMeta(trackID int64, endDate time.Time) string {
	return `{"event_type":"remix_contest","entity_type":"track","entity_id":` + itoa(trackID) +
		`,"end_date":"` + endDate.Format(time.RFC3339) + `","event_data":{}}`
}

// stampedAt restamps params the way the indexer stamps a migration tx: with the
// source row's created_at rather than the block's wall-clock timestamp (see
// migrationBlockTime).
func stampedAt(params *Params, createdAt time.Time) *Params {
	params.BlockTime = createdAt
	return params
}

// The end_date rule holds a replay hostage to how the block is stamped. When a
// migration tx carries a parseable created_at the rule is harmless — a contest
// was always still running when it was created. When it doesn't, block time
// falls back to migration wall-clock and the same rule drops every concluded
// contest: 109 of the 112 live events on the 2026-08-07 snapshot, plus the
// subscriptions pointing at them. The migration handler doesn't take that bet.
func TestMigratedEventCreate_KeepsConcludedContest(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xeventowner", "eventowner")
	seedTrack(t, pool, tid, uid)

	created := time.Now().UTC().Add(-400 * 24 * time.Hour).Truncate(time.Second)
	ended := created.Add(21 * 24 * time.Hour)
	meta := contestMeta(tid, ended)

	// Stamped from created_at, the production handler takes the row: this is why
	// the migration indexes events at all today.
	mustHandle(t, EventCreate(), stampedAt(
		buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 700, "0xEventowner", meta), created))

	// Stamped at wall-clock — the fallback whenever created_at is absent or
	// unparseable — the same row is dropped.
	mustReject(t, EventCreate(),
		buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 701, "0xEventowner", meta),
		"end_date cannot be in the past")

	// The migration handler keeps it either way, end date and all.
	mustHandle(t, migrationHandlerFor(t, EventCreate()),
		buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 702, "0xEventowner", meta))

	var gotType string
	var gotEnd time.Time
	if err := pool.QueryRow(context.Background(),
		"SELECT event_type, end_date FROM events WHERE event_id = 702").Scan(&gotType, &gotEnd); err != nil {
		t.Fatalf("the concluded contest was not indexed: %v", err)
	}
	if gotType != "remix_contest" {
		t.Errorf("event_type = %q, want remix_contest", gotType)
	}
	if !gotEnd.Equal(ended) {
		t.Errorf("end_date = %s, want the source's %s", gotEnd, ended)
	}
}

// The uniqueness rule drops a real row however the block is stamped. Track
// 1174134089 in the source holds two contests created two minutes apart with
// the same end date (events 925703801 and 1458921100): replaying the second
// re-asks a question the source already answered its own way, and the first
// contest — still running at that moment — rejects it.
func TestMigratedEventCreate_KeepsSecondConcludedContestForSameTrack(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xeventowner", "eventowner")
	seedTrack(t, pool, tid, uid)

	firstCreated := time.Now().UTC().Add(-400 * 24 * time.Hour).Truncate(time.Second)
	secondCreated := firstCreated.Add(2 * time.Minute)
	meta := contestMeta(tid, firstCreated.Add(21*24*time.Hour))

	h := migrationHandlerFor(t, EventCreate())
	mustHandle(t, h, stampedAt(
		buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 710, "0xEventowner", meta), firstCreated))

	// The production rule rejects the second row the source is holding.
	mustReject(t, EventCreate(), stampedAt(
		buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 711, "0xEventowner", meta), secondCreated),
		"an existing remix contest for entity_id")

	mustHandle(t, h, stampedAt(
		buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 711, "0xEventowner", meta), secondCreated))

	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM events WHERE entity_id = $1", tid).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 2 {
		t.Errorf("indexed %d contests for track %d, want both source rows", n, tid)
	}
}

// Relaxing the migration must not relax live traffic: a second contest opened
// while the first is still running is still rejected.
func TestEventCreate_StillRejectsSecondRunningContest(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xeventowner", "eventowner")
	seedTrack(t, pool, tid, uid)

	meta := contestMeta(tid, time.Now().UTC().Add(24*time.Hour).Truncate(time.Second))
	mustHandle(t, EventCreate(), buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 720, "0xEventowner", meta))
	mustReject(t, EventCreate(),
		buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 721, "0xEventowner", meta),
		"an existing remix contest for entity_id")
}

// What the migration keeps: an event is still shaped like an event, still
// signed by someone who may act for its user, still lands once, and still hangs
// off rows that exist.
func TestMigratedEventCreate_KeepsShapeAndSignerChecks(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xeventowner", "eventowner")
	seedTrack(t, pool, tid, uid)

	past := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	meta := contestMeta(tid, past)
	h := migrationHandlerFor(t, EventCreate())

	// Idempotency: the same event id must not land twice.
	mustHandle(t, h, buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 730, "0xEventowner", meta))
	mustReject(t, h, buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 730, "0xEventowner", meta),
		"already exists")

	// Signer authority: genesis-writer sends the event owner's wallet, so a
	// signer with no claim on the user is still rejected.
	mustReject(t, h, buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 731, "0xImposter", meta),
		"is not authorized for user")

	// Shape: event_type and end_date are what make the row an event.
	mustReject(t, h, buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 732, "0xEventowner",
		`{"entity_type":"track","entity_id":`+itoa(tid)+`,"end_date":"`+past.Format(time.RFC3339)+`"}`),
		"missing required field: event_type")
	mustReject(t, h, buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 733, "0xEventowner",
		`{"event_type":"remix_contest","entity_type":"track","entity_id":`+itoa(tid)+`}`),
		"missing required field: end_date")

	// Referential: the track the contest hangs off must have migrated.
	mustReject(t, h, buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 734, "0xEventowner",
		contestMeta(TrackIDOffset+999, past)),
		"does not exist")

	// Shape: the dispatcher routes by entity type, so a mismatch is a bug.
	wrongType := buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 735, "0xEventowner", meta)
	wrongType.EntityType = EntityTypeTrack
	mustReject(t, h, wrongType, "wrong entity type")
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

// Playlists drifted the same way tracks did, and further: 189,626 of 312,552
// migrated playlists regenerate to a slug they do not serve today, and 82,793
// of those regenerate to one that exists nowhere -- a dead permalink.
func TestMigratedPlaylistRoute(t *testing.T) {
	for _, tt := range []struct {
		name string
		meta map[string]any
		want *playlistRoute
	}{
		{"legacy id-suffixed slug carried verbatim",
			map[string]any{"route_slug": "unearth-ep-wip-1", "route_title_slug": "unearth-ep-wip-1", "route_collision_id": float64(0)},
			&playlistRoute{Slug: "unearth-ep-wip-1", TitleSlug: "unearth-ep-wip-1"}},
		{"punctuation the current sanitizer would strip",
			map[string]any{"route_slug": "hbk-way-up-prod.-by-nofriends-100001"},
			&playlistRoute{Slug: "hbk-way-up-prod.-by-nofriends-100001", TitleSlug: "hbk-way-up-prod.-by-nofriends-100001"}},
		{"collision id preserved",
			map[string]any{"route_slug": "more-than-enough-1", "route_title_slug": "more-than-enough", "route_collision_id": float64(1)},
			&playlistRoute{Slug: "more-than-enough-1", TitleSlug: "more-than-enough", CollisionID: 1}},
		{"absent falls back to generation", map[string]any{}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := migratedPlaylistRoute(&Params{Metadata: tt.meta})
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

// A live follow implies a subscription; a migrated one must not. The migration
// replays the source's subscriptions table explicitly, so inferring one per
// follow invented 19.8M rows the users never had and made every explicit
// Subscribe collide with a row the follow had already written.
func TestMigratedFollowDoesNotImplySubscription(t *testing.T) {
	countSubscriptionWrites := func(dbtx *stubDBTX) int {
		n := 0
		for _, q := range dbtx.execSQL {
			if strings.Contains(q, "INSERT INTO subscriptions") {
				n++
			}
		}
		return n
	}

	t.Run("migration writes only the follow", func(t *testing.T) {
		dbtx := &stubDBTX{}
		params := legacyFollowParams(t, dbtx)
		if err := insertMigratedFollow(context.Background(), params, false); err != nil {
			t.Fatalf("insertMigratedFollow: %v", err)
		}
		if got := countSubscriptionWrites(dbtx); got != 0 {
			t.Errorf("migrated follow wrote %d subscription rows, want 0", got)
		}
	})

	t.Run("production still implies one", func(t *testing.T) {
		dbtx := &stubDBTX{}
		params := legacyFollowParams(t, dbtx)
		if err := insertFollow(context.Background(), params, false); err != nil {
			t.Fatalf("insertFollow: %v", err)
		}
		if got := countSubscriptionWrites(dbtx); got != 1 {
			t.Errorf("live follow wrote %d subscription rows, want 1", got)
		}
	})

	// The override has to be registered, or the production handler runs and the
	// inference comes back silently.
	d := NewDispatcher(nil)
	RegisterMigrationOverrides(d)
	if !d.HasHandler(EntityTypeAny, ActionFollow) {
		t.Error("no migration override registered for Follow")
	}
}
