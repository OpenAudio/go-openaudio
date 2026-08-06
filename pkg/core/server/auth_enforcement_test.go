package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"
)

// authOnlyRules exercises signer authorization on its own. Content
// authorization has its own coverage in content_auth_state_test.go, and
// leaving it off here keeps these cases focused on the signer predicate.
var authOnlyRules = config.Rules{AuthEnforced: true}

// A later tx in the same proposal must see an earlier tx's auth effects, and
// none of it may touch the base store: only FinalizeBlock's projection
// commits state.
func TestOverlaySequencing(t *testing.T) {
	base := newMemAuthStore()
	overlay := newOverlayAuthStore(base)

	mustProject(t, overlay, userCreateTx(1, "0xw1", "alice"))
	mustProject(t, overlay, trackCreateTx(1, 2_000_001, "0xw1"))

	if len(base.users) != 0 || len(base.entities) != 0 {
		t.Fatal("overlay writes must not reach the base store")
	}

	// A fresh overlay over the same (uncommitted) base is back to square one:
	// the user does not exist, so the track create is rejected.
	fresh := newOverlayAuthStore(base)
	mustSkip(t, fresh, trackCreateTx(1, 2_000_002, "0xw1"), "does not exist")
}

// Copy-on-write: mutations of base-resident rows live only in the overlay,
// and liveness checks read the overlay's version.
func TestOverlayCopyOnWrite(t *testing.T) {
	ctx := context.Background()
	base := newMemAuthStore()
	mustProject(t, base, userCreateTx(1, "0xw1", "alice"))

	overlay := newOverlayAuthStore(base)
	mustProject(t, overlay, authTx{
		UserID: 1, EntityType: "User", Action: "Update", Signer: "0xw1",
		meta: map[string]any{"is_deactivated": true},
	})

	if base.users[1].Deactivated {
		t.Fatal("deactivation leaked into the base store")
	}
	if u, ok, _ := overlay.GetUser(ctx, 1); !ok || !u.Deactivated {
		t.Fatal("overlay must read its own deactivation")
	}
	if active, _ := overlay.ActiveWalletExists(ctx, "0xw1"); active {
		t.Fatal("overlay must not report a deactivated wallet as active")
	}
	if active, _ := base.ActiveWalletExists(ctx, "0xw1"); !active {
		t.Fatal("base store must be unchanged")
	}
}

func enforcementTestServer() *Server {
	return &Server{
		config: &config.Config{
			AcdcEntityManagerAddress: config.DevAcdcAddress,
			AcdcChainID:              config.DevAcdcChainID,
		},
	}
}

func signedManageEntity(t *testing.T, s *Server, em *v1.ManageEntityLegacy) *v1.ManageEntityLegacy {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := SignManageEntity(s.config, em, key); err != nil {
		t.Fatal(err)
	}
	if err := InjectSigner(s.config, em); err != nil {
		t.Fatal(err)
	}
	return em
}

// The signer field is only a claim until it is checked against the EIP-712
// recovery; enforcement must reject a transaction whose Signer names a wallet
// the signature does not prove.
func TestValidateManageEntityAuthSignerRecovery(t *testing.T) {
	ctx := context.Background()
	s := enforcementTestServer()

	em := signedManageEntity(t, s, &v1.ManageEntityLegacy{
		UserId:     1,
		EntityType: "User",
		Action:     "Create",
		Metadata:   `{"handle":"alice"}`,
		Nonce:      "0x0000000000000000000000000000000000000000000000000000000000000001",
	})

	// Legitimate: recovered signer matches, user is new, projection applies.
	if err := s.validateManageEntityAuth(ctx, authOnlyRules, newOverlayAuthStore(newMemAuthStore()), em); err != nil {
		t.Fatalf("valid tx rejected: %v", err)
	}

	// Forged signer field: same signature, different claimed wallet.
	forged := proto.Clone(em).(*v1.ManageEntityLegacy)
	forged.Signer = "0x000000000000000000000000000000000000dEaD"
	err := s.validateManageEntityAuth(ctx, authOnlyRules, newOverlayAuthStore(newMemAuthStore()), forged)
	if err == nil || !authRejected(err) {
		t.Fatalf("expected deterministic rejection for forged signer, got %v", err)
	}

	// No signature at all.
	unsigned := &v1.ManageEntityLegacy{UserId: 2, EntityType: "User", Action: "Create"}
	err = s.validateManageEntityAuth(ctx, authOnlyRules, newOverlayAuthStore(newMemAuthStore()), unsigned)
	if err == nil || !authRejected(err) {
		t.Fatalf("expected deterministic rejection for missing signature, got %v", err)
	}
}

// A projection rejection surfaces as an authRejectionError (deterministic —
// vote the proposal invalid); a store failure surfaces as a plain error (this
// node cannot tell — report unknown). Conflating them would let a local db
// blip vote down a valid proposal. authRejected must also survive wrapping.
func TestValidateManageEntityAuthErrorClassification(t *testing.T) {
	ctx := context.Background()
	s := enforcementTestServer()

	base := newMemAuthStore()
	em := signedManageEntity(t, s, &v1.ManageEntityLegacy{
		UserId:     1,
		EntityType: "User",
		Action:     "Create",
		Nonce:      "0x0000000000000000000000000000000000000000000000000000000000000002",
	})

	// Seed user 1 so the create is a projection-level rejection.
	mustProject(t, base, userCreateTx(1, "0xother", "someone"))
	err := s.validateManageEntityAuth(ctx, authOnlyRules, newOverlayAuthStore(base), em)
	if err == nil || !authRejected(err) {
		t.Fatalf("expected rejection for duplicate user, got %v", err)
	}
	if !authRejected(fmt.Errorf("wrapped: %w", err)) {
		t.Fatal("authRejected must see through error wrapping")
	}

	// A failing store must not read as a rejection.
	err = s.validateManageEntityAuth(ctx, authOnlyRules, newOverlayAuthStore(&failingAuthStore{}), em)
	if err == nil || authRejected(err) {
		t.Fatalf("expected store failure to surface as a plain error, got %v", err)
	}
}

// failingAuthStore errors on every read, standing in for an unreachable db.
type failingAuthStore struct{ memAuthStore }

func (f *failingAuthStore) GetUser(context.Context, int64) (authUserRow, bool, error) {
	return authUserRow{}, false, errors.New("store unavailable")
}

// Unauthorized txs must be filtered from proposals; authorized ones pass, in
// dependency order within a single proposal.
func TestProposalOverlayDependencyOrder(t *testing.T) {
	base := newMemAuthStore()
	overlay := newOverlayAuthStore(base)

	// Grant created earlier in the proposal authorizes a track create later
	// in the same proposal.
	mustProject(t, base, userCreateTx(1, "0xw1", "alice"))
	mustProject(t, base, userCreateTx(2, "0xw2", "bob"))
	mustProject(t, overlay, authTx{
		UserID: 1, EntityType: "Grant", Action: "Create", Signer: "0xw1",
		meta: map[string]any{"grantee_address": "0xw2"},
	})
	mustProject(t, overlay, authTx{
		UserID: 2, EntityType: "Grant", Action: "Approve", Signer: "0xw2",
		meta: map[string]any{"grantee_address": "0xw2", "grantor_user_id": float64(1)},
	})
	mustProject(t, overlay, trackCreateTx(1, 2_000_005, "0xw2"))

	if _, ok := base.grants[memGrantKey{"0xw2", 1}]; ok {
		t.Fatal("proposal-time grant must not reach the base store")
	}
}
