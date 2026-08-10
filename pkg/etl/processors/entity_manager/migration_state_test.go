package entity_manager

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// insertArgsByColumn pairs an INSERT's column list with its VALUES list and
// resolves each `$n` placeholder to the argument that was bound to it, so a test
// can assert on "the value written to coin_flair_mint" rather than on an
// argument index that shifts every time a column is added.
func insertArgsByColumn(t *testing.T, sql string, args []any) map[string]any {
	t.Helper()
	cols := bracketedList(t, sql, `(?s)INSERT INTO [a-z_]+ \(([^)]*)\)`)
	vals := bracketedList(t, sql, `(?s)VALUES \(([^)]*)\)`)
	if len(cols) != len(vals) {
		t.Fatalf("INSERT lists %d columns but binds %d values", len(cols), len(vals))
	}

	placeholder := regexp.MustCompile(`^\$(\d+)$`)
	out := make(map[string]any, len(cols))
	for i, col := range cols {
		m := placeholder.FindStringSubmatch(vals[i])
		if m == nil {
			// a literal such as `true`; not something a caller can influence
			continue
		}
		n := 0
		for _, c := range m[1] {
			n = n*10 + int(c-'0')
		}
		if n < 1 || n > len(args) {
			t.Fatalf("column %s binds %s but only %d args were passed", col, vals[i], len(args))
		}
		out[col] = args[n-1]
	}
	return out
}

func bracketedList(t *testing.T, sql, pattern string) []string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(sql)
	if m == nil {
		t.Fatalf("could not locate %q in the statement -- it was reshaped; update this helper", pattern)
	}
	var out []string
	for _, tok := range strings.Split(m[1], ",") {
		if i := strings.Index(tok, "--"); i >= 0 {
			tok = tok[:i]
		}
		if tok = strings.TrimSpace(tok); tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

// lastInsert returns the column-keyed arguments of the statement the handler
// issued.
func lastInsert(t *testing.T, dbtx *stubDBTX) map[string]any {
	t.Helper()
	if len(dbtx.execSQL) == 0 {
		t.Fatal("expected an INSERT to be issued")
	}
	return insertArgsByColumn(t, dbtx.execSQL[len(dbtx.execSQL)-1], dbtx.execArgs)
}

// A migrated Create is an account's whole history collapsed into one
// transaction, so profile settings a live client only ever sends on Update have
// to survive it. Without this the migration drops 3,816 payout wallets, 425
// profile types and 96 coin flair mints.
func TestMigratedUserCreate_PipesProfileSettings(t *testing.T) {
	tests := []struct {
		name                            string
		metadata                        string
		wantPayout, wantType, wantFlair any
	}{
		{
			"all set",
			`{"handle":"a","spl_usdc_payout_wallet":"So11111111111111111111111111111111111111112","profile_type":"label","coin_flair_mint":"MintAddr1111111111111111111111111111111111"}`,
			"So11111111111111111111111111111111111111112", "label", "MintAddr1111111111111111111111111111111111",
		},
		// Absent keys must insert NULL, not an empty string.
		{"absent", `{"handle":"b"}`, nil, nil, nil},
		// The source holds 376 empty-string coin_flair_mint rows; an empty
		// string is the absent value, not a set one.
		{"empty strings", `{"handle":"c","spl_usdc_payout_wallet":"","coin_flair_mint":""}`, nil, nil, nil},
		// profile_type is a Postgres enum: an unknown label would fail the
		// insert, so it is dropped rather than written.
		{"unknown profile type", `{"handle":"d","profile_type":"conglomerate"}`, nil, nil, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbtx := &stubDBTX{}
			if err := migratedUserCreate().Handle(context.Background(), legacyUserParams(t, dbtx, tt.metadata)); err != nil {
				t.Fatalf("handle: %v", err)
			}
			got := lastInsert(t, dbtx)
			for col, want := range map[string]any{
				"spl_usdc_payout_wallet": tt.wantPayout,
				"profile_type":           tt.wantType,
				"coin_flair_mint":        tt.wantFlair,
			} {
				if got[col] != want {
					t.Errorf("users.%s = %#v, want %#v", col, got[col], want)
				}
			}
		})
	}
}

// The three account-state flags stay where the migration tests expect them, so
// adding columns to the INSERT never quietly reassigns them.
func TestInsertUserWithState_BindsFlagsToTheirOwnColumns(t *testing.T) {
	dbtx := &stubDBTX{}
	params := legacyUserParams(t, dbtx, `{"handle":"a"}`)
	if err := insertUserWithState(context.Background(), params, userState{IsVerified: true, IsAvailable: true}); err != nil {
		t.Fatalf("insertUserWithState: %v", err)
	}
	got := lastInsert(t, dbtx)
	for col, want := range map[string]any{"is_verified": true, "is_deactivated": false, "is_available": true} {
		if got[col] != want {
			t.Errorf("users.%s = %#v, want %#v", col, got[col], want)
		}
	}
}

// A migrated grant carries the approval the source recorded. is_approved is
// three-valued and derived on a production create from the grantee's type, so a
// user-to-user manager grant replays as NULL without this -- and ValidateSigner
// refuses a non-app grantee whose grant is not approved, which would strip 493
// managers of access to the accounts they manage.
func TestMigratedGrantCreate_PipesApproval(t *testing.T) {
	tests := []struct {
		name         string
		metadata     string
		wantApproved any
		wantRevoked  any
	}{
		// The common repaired case: approved manager grant, grantee is not an
		// app (the stub reports no developer app), so the derivation would give
		// NULL.
		{"approved manager grant", `{"grantee_address":"0xGrantee","is_revoked":false,"is_approved":true}`, true, false},
		// Approved and revoked together. No Grant/Approve transaction could
		// reproduce this pair: approving forces is_revoked back to false.
		{"approved then revoked", `{"grantee_address":"0xGrantee","is_revoked":true,"is_approved":true}`, true, true},
		// A rejected grant records false explicitly; `omitempty` on a *bool
		// keeps it in the metadata, and false must not be read as absent.
		{"rejected", `{"grantee_address":"0xGrantee","is_revoked":true,"is_approved":false}`, false, true},
		// Absent keeps the production derivation, which is what the 79
		// source-NULL grants need.
		{"absent", `{"grantee_address":"0xGrantee","is_revoked":false}`, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbtx := &stubDBTX{}
			params := legacyUserParams(t, dbtx, tt.metadata)
			params.EntityType = EntityTypeGrant
			if err := migratedGrantCreate().Handle(context.Background(), params); err != nil {
				t.Fatalf("handle: %v", err)
			}
			got := lastInsert(t, dbtx)
			if !approvalEquals(got["is_approved"], tt.wantApproved) {
				t.Errorf("grants.is_approved = %#v, want %#v", got["is_approved"], tt.wantApproved)
			}
			if got["is_revoked"] != tt.wantRevoked {
				t.Errorf("grants.is_revoked = %#v, want %#v", got["is_revoked"], tt.wantRevoked)
			}
		})
	}
}

// A live client must not be able to grant itself an approved manager
// relationship by posting is_approved in metadata: approval goes through the
// Grant/Approve action, which the grantee has to sign. The production insert
// derives the value and ignores the key entirely.
func TestProductionInsertGrant_IgnoresApprovalMetadata(t *testing.T) {
	for _, meta := range []string{
		`{"grantee_address":"0xGrantee","is_approved":true}`,
		`{"grantee_address":"0xGrantee","is_approved":true,"is_revoked":true}`,
	} {
		dbtx := &stubDBTX{}
		params := legacyUserParams(t, dbtx, meta)
		params.EntityType = EntityTypeGrant
		if err := insertGrant(context.Background(), params); err != nil {
			t.Fatalf("insertGrant: %v", err)
		}
		got := lastInsert(t, dbtx)
		// The stub reports the grantee is not a developer app, so the derived
		// value is NULL.
		if !approvalEquals(got["is_approved"], nil) {
			t.Errorf("metadata %s: grants.is_approved = %#v, want nil -- metadata must not be trusted here", meta, got["is_approved"])
		}
		if got["is_revoked"] != false {
			t.Errorf("metadata %s: grants.is_revoked = %#v, want false -- a new grant is never created revoked", meta, got["is_revoked"])
		}
	}
}

// approvalEquals compares the *bool bound to is_approved against a plain want.
func approvalEquals(got any, want any) bool {
	p, _ := got.(*bool)
	if want == nil {
		return p == nil
	}
	return p != nil && *p == want.(bool)
}
