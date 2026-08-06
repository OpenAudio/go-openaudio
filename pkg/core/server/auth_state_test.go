package server

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
)

// memAuthStore is a map-backed authStore so projection logic tests run
// without postgres.
type memAuthStore struct {
	users    map[int64]authUserRow
	grants   map[memGrantKey]authGrantRow
	apps     map[string]authAppRow
	entities map[memEntityKey]authEntityRow
	cids     map[string]authCidRow
}

type memGrantKey struct {
	grantee string
	userID  int64
}

type memEntityKey struct {
	entityType string
	entityID   int64
}

func newMemAuthStore() *memAuthStore {
	return &memAuthStore{
		users:    map[int64]authUserRow{},
		grants:   map[memGrantKey]authGrantRow{},
		apps:     map[string]authAppRow{},
		entities: map[memEntityKey]authEntityRow{},
		cids:     map[string]authCidRow{},
	}
}

func (m *memAuthStore) GetCid(_ context.Context, cid string) (authCidRow, bool, error) {
	c, ok := m.cids[cid]
	return c, ok, nil
}

func (m *memAuthStore) InsertCid(_ context.Context, cid, uploaderAddress, attestedBy string, _ int64) error {
	if _, ok := m.cids[cid]; ok {
		return nil
	}
	m.cids[cid] = authCidRow{UploaderAddress: uploaderAddress, AttestedBy: attestedBy}
	return nil
}

func (m *memAuthStore) GetUser(_ context.Context, userID int64) (authUserRow, bool, error) {
	u, ok := m.users[userID]
	return u, ok, nil
}

func (m *memAuthStore) GetUserIDByWallet(_ context.Context, wallet string) (int64, bool, error) {
	found := false
	var id int64
	for uid, u := range m.users {
		if u.Wallet == wallet && !u.Deactivated && (!found || uid < id) {
			id, found = uid, true
		}
	}
	return id, found, nil
}

func (m *memAuthStore) WalletExists(_ context.Context, wallet string) (bool, error) {
	for _, u := range m.users {
		if u.Wallet == wallet {
			return true, nil
		}
	}
	return false, nil
}

func (m *memAuthStore) ActiveWalletExists(_ context.Context, wallet string) (bool, error) {
	for _, u := range m.users {
		if u.Wallet == wallet && !u.Deactivated {
			return true, nil
		}
	}
	return false, nil
}

func (m *memAuthStore) HandleExists(_ context.Context, handleLC string) (bool, error) {
	for _, u := range m.users {
		if u.HandleLC == handleLC {
			return true, nil
		}
	}
	return false, nil
}

func (m *memAuthStore) GetGrant(_ context.Context, grantee string, userID int64) (authGrantRow, bool, error) {
	g, ok := m.grants[memGrantKey{grantee, userID}]
	if ok && g.Approved != nil {
		approved := *g.Approved
		g.Approved = &approved
	}
	return g, ok, nil
}

func (m *memAuthStore) GetApp(_ context.Context, address string) (authAppRow, bool, error) {
	a, ok := m.apps[address]
	return a, ok, nil
}

func (m *memAuthStore) GetEntity(_ context.Context, entityType string, entityID int64) (authEntityRow, bool, error) {
	e, ok := m.entities[memEntityKey{entityType, entityID}]
	return e, ok, nil
}

func (m *memAuthStore) InsertUser(_ context.Context, userID int64, wallet, handleLC string, deactivated bool) error {
	if _, ok := m.users[userID]; !ok {
		m.users[userID] = authUserRow{Wallet: wallet, HandleLC: handleLC, Deactivated: deactivated}
	}
	return nil
}

func (m *memAuthStore) SetUserHandle(_ context.Context, userID int64, handleLC string) error {
	if u, ok := m.users[userID]; ok {
		u.HandleLC = handleLC
		m.users[userID] = u
	}
	return nil
}

func (m *memAuthStore) SetUserDeactivated(_ context.Context, userID int64, deactivated bool) error {
	if u, ok := m.users[userID]; ok {
		u.Deactivated = deactivated
		m.users[userID] = u
	}
	return nil
}

func (m *memAuthStore) UpsertGrant(_ context.Context, grantee string, userID int64, approved *bool, revoked bool) error {
	m.grants[memGrantKey{grantee, userID}] = authGrantRow{Approved: approved, Revoked: revoked}
	return nil
}

func (m *memAuthStore) UpsertApp(_ context.Context, address string, ownerID int64) error {
	m.apps[address] = authAppRow{OwnerID: ownerID}
	return nil
}

func (m *memAuthStore) SetAppDeleted(_ context.Context, address string) error {
	if a, ok := m.apps[address]; ok {
		a.Deleted = true
		m.apps[address] = a
	}
	return nil
}

func (m *memAuthStore) InsertEntity(_ context.Context, entityType string, entityID, ownerID int64, deleted bool) error {
	k := memEntityKey{entityType, entityID}
	if _, ok := m.entities[k]; !ok {
		m.entities[k] = authEntityRow{OwnerID: ownerID, Deleted: deleted}
	}
	return nil
}

func (m *memAuthStore) SetEntityDeleted(_ context.Context, entityType string, entityID int64) error {
	k := memEntityKey{entityType, entityID}
	if e, ok := m.entities[k]; ok {
		e.Deleted = true
		m.entities[k] = e
	}
	return nil
}

// --- helpers ---

func mustProject(t *testing.T, st authStore, tx authTx) {
	t.Helper()
	if err := applyAuthProjection(context.Background(), st, tx); err != nil {
		t.Fatalf("expected projection to apply: %v", err)
	}
}

func mustSkip(t *testing.T, st authStore, tx authTx, wantReason string) {
	t.Helper()
	err := applyAuthProjection(context.Background(), st, tx)
	if err == nil {
		t.Fatalf("expected projection to skip (%s), but it applied", wantReason)
	}
	if !isAuthValidationError(err) {
		t.Fatalf("expected a rule rejection (%s), got store error: %v", wantReason, err)
	}
	if !strings.Contains(err.Error(), wantReason) {
		t.Fatalf("expected rejection containing %q, got %q", wantReason, err.Error())
	}
}

func userCreateTx(userID int64, wallet, handle string) authTx {
	return authTx{
		UserID: userID, EntityType: "User", Action: "Create", Signer: wallet,
		meta: map[string]any{"handle": handle},
	}
}

func trackCreateTx(userID, trackID int64, signer string) authTx {
	return authTx{
		UserID: userID, EntityType: "Track", EntityID: trackID, Action: "Create", Signer: signer,
		meta: map[string]any{"owner_id": float64(userID)},
	}
}

// --- tests ---

func TestProjectUserCreate(t *testing.T) {
	st := newMemAuthStore()

	mustProject(t, st, userCreateTx(1, "0xWalletONE", "Alice"))
	u := st.users[1]
	if u.Wallet != "0xwalletone" || u.HandleLC != "alice" {
		t.Fatalf("user not recorded lowercased: %+v", u)
	}

	// Same wallet again: live create skipped, migration create allowed.
	mustSkip(t, st, userCreateTx(2, "0xWalletONE", "Bob"), "already in use")
	migrated := userCreateTx(3, "0xWalletONE", "Carol")
	migrated.Migration = true
	mustProject(t, st, migrated)

	// Duplicate handle on a fresh wallet: skipped.
	mustSkip(t, st, userCreateTx(4, "0xWalletFOUR", "ALICE"), "already exists")

	// Same user id replayed: idempotent skip.
	mustSkip(t, st, userCreateTx(1, "0xOther", "Other"), "user 1 already exists")
}

func TestProjectSignerViaAppGrant(t *testing.T) {
	st := newMemAuthStore()
	ctx := context.Background()

	mustProject(t, st, userCreateTx(1, "0xw1", "alice"))
	mustProject(t, st, authTx{
		UserID: 1, EntityType: "DeveloperApp", Action: "Create", Signer: "0xw1",
		meta: map[string]any{"address": "0xAPP"},
	})
	// Grant to the app is auto-approved at creation.
	mustProject(t, st, authTx{
		UserID: 1, EntityType: "Grant", Action: "Create", Signer: "0xw1",
		meta: map[string]any{"grantee_address": "0xAPP"},
	})
	g := st.grants[memGrantKey{"0xapp", 1}]
	if g.Approved == nil || !*g.Approved {
		t.Fatalf("app grant should be auto-approved: %+v", g)
	}

	// The app can now act for user 1.
	mustProject(t, st, trackCreateTx(1, 2_000_001, "0xapp"))
	if e := st.entities[memEntityKey{"Track", 2_000_001}]; e.OwnerID != 1 {
		t.Fatalf("track ownership not recorded: %+v", e)
	}

	// Revoke the grant; the app loses authority.
	mustProject(t, st, authTx{
		UserID: 1, EntityType: "Grant", Action: "Delete", Signer: "0xw1",
		meta: map[string]any{"grantee_address": "0xapp"},
	})
	mustSkip(t, st, trackCreateTx(1, 2_000_002, "0xapp"), "not authorized")

	// Deleting the app makes it an invalid grantee even under a fresh grant.
	mustProject(t, st, authTx{
		UserID: 1, EntityType: "Grant", Action: "Create", Signer: "0xw1",
		meta: map[string]any{"grantee_address": "0xapp"},
	})
	mustProject(t, st, authTx{
		UserID: 1, EntityType: "DeveloperApp", Action: "Delete", Signer: "0xw1",
		meta: map[string]any{"address": "0xapp"},
	})
	if err := validateAuthSigner(ctx, st, 1, "0xapp"); err == nil {
		t.Fatal("deleted app must not remain a valid signer")
	}
}

func TestProjectUserToUserGrant(t *testing.T) {
	st := newMemAuthStore()

	mustProject(t, st, userCreateTx(1, "0xw1", "alice"))
	mustProject(t, st, userCreateTx(2, "0xw2", "bob"))

	// User 1 grants user 2's wallet manager access: starts pending.
	mustProject(t, st, authTx{
		UserID: 1, EntityType: "Grant", Action: "Create", Signer: "0xw1",
		meta: map[string]any{"grantee_address": "0xW2"},
	})
	mustSkip(t, st, trackCreateTx(1, 2_000_010, "0xw2"), "not approved")

	// Grantee approves (tx user id is the approving manager, grantor in metadata).
	mustProject(t, st, authTx{
		UserID: 2, EntityType: "Grant", Action: "Approve", Signer: "0xw2",
		meta: map[string]any{"grantee_address": "0xw2", "grantor_user_id": float64(1)},
	})
	mustProject(t, st, trackCreateTx(1, 2_000_010, "0xw2"))

	// The manager can revoke their own management relationship.
	mustProject(t, st, authTx{
		UserID: 1, EntityType: "Grant", Action: "Delete", Signer: "0xw2",
		meta: map[string]any{"grantee_address": "0xw2"},
	})
	mustSkip(t, st, trackCreateTx(1, 2_000_011, "0xw2"), "not authorized")

	// A rejected pending grant ends revoked and unapproved.
	mustProject(t, st, authTx{
		UserID: 1, EntityType: "Grant", Action: "Create", Signer: "0xw1",
		meta: map[string]any{"grantee_address": "0xw2"},
	})
	mustProject(t, st, authTx{
		UserID: 2, EntityType: "Grant", Action: "Reject", Signer: "0xw2",
		meta: map[string]any{"grantee_address": "0xw2", "grantor_user_id": float64(1)},
	})
	g := st.grants[memGrantKey{"0xw2", 1}]
	if !g.Revoked || g.Approved == nil || *g.Approved {
		t.Fatalf("rejected grant should be revoked and unapproved: %+v", g)
	}
}

func TestProjectEntityOwnership(t *testing.T) {
	st := newMemAuthStore()

	mustProject(t, st, userCreateTx(1, "0xw1", "alice"))
	mustProject(t, st, userCreateTx(2, "0xw2", "bob"))
	mustProject(t, st, trackCreateTx(1, 2_000_001, "0xw1"))

	// Duplicate id, mismatched metadata owner, below-offset id: all skipped.
	mustSkip(t, st, trackCreateTx(2, 2_000_001, "0xw2"), "already exists")
	bad := trackCreateTx(1, 2_000_002, "0xw1")
	bad.meta["owner_id"] = float64(9)
	mustSkip(t, st, bad, "owner_id")
	mustSkip(t, st, trackCreateTx(1, 42, "0xw1"), "below offset")

	// Migration replays legacy ids below the offset, including deleted rows.
	legacy := trackCreateTx(1, 42, "0xw1")
	legacy.Migration = true
	legacy.meta["is_delete"] = true
	mustProject(t, st, legacy)
	if e := st.entities[memEntityKey{"Track", 42}]; !e.Deleted {
		t.Fatalf("migrated deleted track not recorded: %+v", e)
	}

	// Delete: only the owner (or their delegate) may delete.
	mustSkip(t, st, authTx{
		UserID: 2, EntityType: "Track", EntityID: 2_000_001, Action: "Delete", Signer: "0xw2",
	}, "does not belong to user")
	mustProject(t, st, authTx{
		UserID: 1, EntityType: "Track", EntityID: 2_000_001, Action: "Delete", Signer: "0xw1",
	})
	if e := st.entities[memEntityKey{"Track", 2_000_001}]; !e.Deleted {
		t.Fatalf("track delete not recorded: %+v", e)
	}
}

func TestProjectUserUpdateDeactivation(t *testing.T) {
	st := newMemAuthStore()
	mustProject(t, st, userCreateTx(1, "0xw1", "alice"))
	mustProject(t, st, userCreateTx(2, "0xw2", "bob"))

	// Only an authorized signer may update.
	mustSkip(t, st, authTx{
		UserID: 1, EntityType: "User", Action: "Update", Signer: "0xw2",
		meta: map[string]any{"is_deactivated": true},
	}, "not authorized")

	mustProject(t, st, authTx{
		UserID: 1, EntityType: "User", Action: "Update", Signer: "0xw1",
		meta: map[string]any{"is_deactivated": true},
	})
	if !st.users[1].Deactivated {
		t.Fatal("deactivation not recorded")
	}
	// A deactivated wallet is no longer a valid user-to-user grantee signer.
	if ok, _ := st.ActiveWalletExists(context.Background(), "0xw1"); ok {
		t.Fatal("deactivated wallet should not read as active")
	}
}

// Pairs the projection does not track must be accepted silently — nil, not a
// rule rejection — since callers treat nil as "no auth opinion".
func TestProjectUntrackedPairIsNil(t *testing.T) {
	st := newMemAuthStore()
	if err := applyAuthProjection(context.Background(), st, authTx{
		UserID: 1, EntityType: "Follow", Action: "Create", Signer: "0xw1",
	}); err != nil {
		t.Fatalf("untracked pair must project to nil, got %v", err)
	}
}

// The proto conversion must unwrap the {"cid": ..., "data": {...}} metadata
// envelope the same way the ETL's NewParams does.
func TestAuthTxMetadataEnvelope(t *testing.T) {
	em := &v1.ManageEntityLegacy{
		UserId:     7,
		EntityType: "Grant",
		Action:     "Create",
		Signer:     "0xW7",
		Metadata:   `{"cid":"Qm123","data":{"grantee_address":"0xApp"}}`,
	}
	tx := authTxFromManageEntity(em)
	if got := tx.metaString("grantee_address"); got != "0xApp" {
		t.Fatalf("envelope not unwrapped, got %q", got)
	}

	flat := authTxFromManageEntity(&v1.ManageEntityLegacy{Metadata: `{"grantee_address":"0xApp"}`})
	if got := flat.metaString("grantee_address"); got != "0xApp" {
		t.Fatalf("flat metadata not parsed, got %q", got)
	}

	if bad := authTxFromManageEntity(&v1.ManageEntityLegacy{Metadata: `not json`}); bad.meta != nil {
		t.Fatal("unparseable metadata must read as absent")
	}
}
