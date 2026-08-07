package server

import (
	"context"
)

// overlayAuthStore is the ephemeral implementation of authStore
// (auth_state.go): proposal/mempool validation points the auth projection at
// one of these, layered over committed state, so accepted transactions'
// effects accumulate in memory for later transactions in the same proposal
// and the whole thing is discarded when validation ends. FinalizeBlock uses
// the durable dbAuthStore (auth_store_db.go) instead; same transition
// function, different store.
//
// Copy-on-write: mutations materialize the row into the overlay; reads prefer
// overlay rows; the base store is never written. Wallet/handle existence
// scans consult both layers — with legacy duplicate wallets a base row
// shadowed by a deactivating overlay write can be misjudged, which is
// accepted: the outcome is deterministic across validators, and
// FinalizeBlock's sequential projection remains the ground truth for state.
//
// Not safe for concurrent use: a proposal's transactions are validated
// sequentially on purpose, since each may depend on the effects of the one
// before it.
type overlayAuthStore struct {
	base     authStore
	users    map[int64]authUserRow
	grants   map[overlayGrantKey]authGrantRow
	apps     map[string]authAppRow
	entities map[overlayEntityKey]authEntityRow
	cids     map[overlayCidKey]struct{}
}

type overlayGrantKey struct {
	grantee string
	userID  int64
}

type overlayEntityKey struct {
	entityType string
	entityID   int64
}

type overlayCidKey struct {
	cid    string
	userID int64
}

func newOverlayAuthStore(base authStore) *overlayAuthStore {
	return &overlayAuthStore{
		base:     base,
		users:    map[int64]authUserRow{},
		grants:   map[overlayGrantKey]authGrantRow{},
		apps:     map[string]authAppRow{},
		entities: map[overlayEntityKey]authEntityRow{},
		cids:     map[overlayCidKey]struct{}{},
	}
}

// newProposalAuthOverlay returns the overlay used to validate one proposal's
// (or one mempool submission's) ManageEntity transactions against committed
// auth state.
func (s *Server) newProposalAuthOverlay() *overlayAuthStore {
	return newOverlayAuthStore(&dbAuthStore{q: s.db})
}

func (o *overlayAuthStore) GetUser(ctx context.Context, userID int64) (authUserRow, bool, error) {
	if u, ok := o.users[userID]; ok {
		return u, true, nil
	}
	return o.base.GetUser(ctx, userID)
}

func (o *overlayAuthStore) GetUserIDByWallet(ctx context.Context, wallet string) (int64, bool, error) {
	found := false
	var id int64
	for uid, u := range o.users {
		if u.Wallet == wallet && !u.Deactivated && (!found || uid < id) {
			id, found = uid, true
		}
	}
	if found {
		return id, true, nil
	}
	return o.base.GetUserIDByWallet(ctx, wallet)
}

func (o *overlayAuthStore) WalletExists(ctx context.Context, wallet string) (bool, error) {
	for _, u := range o.users {
		if u.Wallet == wallet {
			return true, nil
		}
	}
	return o.base.WalletExists(ctx, wallet)
}

func (o *overlayAuthStore) ActiveWalletExists(ctx context.Context, wallet string) (bool, error) {
	hasWallet := false
	for _, u := range o.users {
		if u.Wallet == wallet {
			hasWallet = true
			if !u.Deactivated {
				return true, nil
			}
		}
	}
	if hasWallet {
		// The overlay's rows for this wallet are all deactivated and are the
		// freshest view; see the duplicate-wallet caveat on the type.
		return false, nil
	}
	return o.base.ActiveWalletExists(ctx, wallet)
}

func (o *overlayAuthStore) HandleExists(ctx context.Context, handleLC string) (bool, error) {
	for _, u := range o.users {
		if u.HandleLC == handleLC {
			return true, nil
		}
	}
	return o.base.HandleExists(ctx, handleLC)
}

func (o *overlayAuthStore) GetGrant(ctx context.Context, grantee string, userID int64) (authGrantRow, bool, error) {
	if g, ok := o.grants[overlayGrantKey{grantee, userID}]; ok {
		if g.Approved != nil {
			approved := *g.Approved
			g.Approved = &approved
		}
		return g, true, nil
	}
	return o.base.GetGrant(ctx, grantee, userID)
}

func (o *overlayAuthStore) GetApp(ctx context.Context, address string) (authAppRow, bool, error) {
	if a, ok := o.apps[address]; ok {
		return a, true, nil
	}
	return o.base.GetApp(ctx, address)
}

func (o *overlayAuthStore) GetEntity(ctx context.Context, entityType string, entityID int64) (authEntityRow, bool, error) {
	if e, ok := o.entities[overlayEntityKey{entityType, entityID}]; ok {
		return e, true, nil
	}
	return o.base.GetEntity(ctx, entityType, entityID)
}

func (o *overlayAuthStore) IsCidClaimedByUser(ctx context.Context, cid string, userID int64) (bool, error) {
	if _, ok := o.cids[overlayCidKey{cid, userID}]; ok {
		return true, nil
	}
	return o.base.IsCidClaimedByUser(ctx, cid, userID)
}

func (o *overlayAuthStore) CidIsClaimed(ctx context.Context, cid string) (bool, error) {
	for k := range o.cids {
		if k.cid == cid {
			return true, nil
		}
	}
	return o.base.CidIsClaimed(ctx, cid)
}

func (o *overlayAuthStore) InsertCid(_ context.Context, cid string, uploaderUserID int64, _ string) error {
	o.cids[overlayCidKey{cid, uploaderUserID}] = struct{}{}
	return nil
}

func (o *overlayAuthStore) InsertUser(_ context.Context, userID int64, wallet, handleLC string, deactivated bool) error {
	if _, ok := o.users[userID]; !ok {
		o.users[userID] = authUserRow{Wallet: wallet, HandleLC: handleLC, Deactivated: deactivated}
	}
	return nil
}

// materializeUser copies a base-resident row into the overlay so a mutation
// stays local. Absent users are a no-op, matching the db store's UPDATE.
func (o *overlayAuthStore) materializeUser(ctx context.Context, userID int64) (authUserRow, bool, error) {
	if u, ok := o.users[userID]; ok {
		return u, true, nil
	}
	u, ok, err := o.base.GetUser(ctx, userID)
	if err != nil || !ok {
		return authUserRow{}, false, err
	}
	o.users[userID] = u
	return u, true, nil
}

func (o *overlayAuthStore) SetUserHandle(ctx context.Context, userID int64, handleLC string) error {
	u, ok, err := o.materializeUser(ctx, userID)
	if err != nil || !ok {
		return err
	}
	u.HandleLC = handleLC
	o.users[userID] = u
	return nil
}

func (o *overlayAuthStore) SetUserDeactivated(ctx context.Context, userID int64, deactivated bool) error {
	u, ok, err := o.materializeUser(ctx, userID)
	if err != nil || !ok {
		return err
	}
	u.Deactivated = deactivated
	o.users[userID] = u
	return nil
}

func (o *overlayAuthStore) UpsertGrant(_ context.Context, grantee string, userID int64, approved *bool, revoked bool) error {
	o.grants[overlayGrantKey{grantee, userID}] = authGrantRow{Approved: approved, Revoked: revoked}
	return nil
}

func (o *overlayAuthStore) UpsertApp(_ context.Context, address string, ownerID int64) error {
	o.apps[address] = authAppRow{OwnerID: ownerID}
	return nil
}

func (o *overlayAuthStore) SetAppDeleted(ctx context.Context, address string) error {
	a, ok := o.apps[address]
	if !ok {
		var err error
		a, ok, err = o.base.GetApp(ctx, address)
		if err != nil || !ok {
			return err
		}
	}
	a.Deleted = true
	o.apps[address] = a
	return nil
}

func (o *overlayAuthStore) InsertEntity(_ context.Context, entityType string, entityID, ownerID int64, deleted bool) error {
	k := overlayEntityKey{entityType, entityID}
	if _, ok := o.entities[k]; !ok {
		o.entities[k] = authEntityRow{OwnerID: ownerID, Deleted: deleted}
	}
	return nil
}

func (o *overlayAuthStore) SetEntityDeleted(ctx context.Context, entityType string, entityID int64) error {
	k := overlayEntityKey{entityType, entityID}
	e, ok := o.entities[k]
	if !ok {
		var err error
		e, ok, err = o.base.GetEntity(ctx, entityType, entityID)
		if err != nil || !ok {
			return err
		}
	}
	e.Deleted = true
	o.entities[k] = e
	return nil
}
