package server

import (
	"strings"
	"testing"
)

func TestLiveUserCreateRefusesWhatTheIndexerRefuses(t *testing.T) {
	cases := []struct {
		name   string
		meta   map[string]any
		reason string
	}{
		{"handle with illegal characters", map[string]any{"handle": "someone@example.com"}, "illegal characters"},
		{"handle with a dash", map[string]any{"handle": "some-one"}, "illegal characters"},
		{"handle too long", map[string]any{"handle": strings.Repeat("a", 31)}, "character limit"},
		{"reserved handle", map[string]any{"handle": "Admin"}, "reserved"},
		{"name too long", map[string]any{"handle": "ok", "name": strings.Repeat("n", 33)}, "name exceeds"},
		{"bio too long", map[string]any{"handle": "ok", "bio": strings.Repeat("b", 257)}, "bio exceeds"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newMemAuthStore()
			tx := userCreateTx(1, "0xwallet", "")
			tx.meta = c.meta
			mustSkip(t, st, tx, c.reason)
			if _, ok := st.users[1]; ok {
				t.Fatal("refused create left a user behind")
			}
		})
	}
}

func TestLiveUserCreateAcceptsLimits(t *testing.T) {
	st := newMemAuthStore()
	tx := userCreateTx(1, "0xwallet", strings.Repeat("a", 30))
	tx.meta["name"] = strings.Repeat("é", 32)
	tx.meta["bio"] = strings.Repeat("b", 256)
	mustProject(t, st, tx)

	// Uppercase is lowercased before the format check, as the indexer does.
	mustProject(t, st, userCreateTx(2, "0xwallet2", "Mixed_Case.99"))
}

// The case that halted the mirror: a create the indexer refused for its handle
// must not hold the wallet against the same wallet's later valid create.
func TestRefusedHandleDoesNotHoldTheWallet(t *testing.T) {
	st := newMemAuthStore()
	mustSkip(t, st, userCreateTx(1, "0xWallet", "someone@example.com"), "illegal characters")
	mustProject(t, st, userCreateTx(2, "0xWallet", "someone"))
	if got := st.users[2].Wallet; got != "0xwallet" {
		t.Fatalf("second create not recorded: %q", got)
	}
}

func TestMigratedUserCreateSkipsMetadataRules(t *testing.T) {
	st := newMemAuthStore()
	tx := userCreateTx(1, "0xwallet", "legacy-handle")
	tx.Migration = true
	mustProject(t, st, tx)
}

func TestLiveUserUpdateRefusesBadMetadata(t *testing.T) {
	st := newMemAuthStore()
	mustProject(t, st, userCreateTx(1, "0xwallet", "alice"))

	bad := authTx{UserID: 1, EntityType: "User", Action: "Update", Signer: "0xwallet",
		meta: map[string]any{"handle": "alice@example.com", "is_deactivated": true}}
	mustSkip(t, st, bad, "illegal characters")
	if u := st.users[1]; u.HandleLC != "alice" || u.Deactivated {
		t.Fatalf("refused update changed the user: %+v", u)
	}

	good := authTx{UserID: 1, EntityType: "User", Action: "Update", Signer: "0xwallet",
		meta: map[string]any{"handle": "Alice2"}}
	mustProject(t, st, good)
	if got := st.users[1].HandleLC; got != "alice2" {
		t.Fatalf("handle not updated: %q", got)
	}
}
