package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
)

// This file maintains the consensus-side authorization state (core_auth_*
// tables): a projection of the authorization-relevant subset of ManageEntity
// effects, applied in FinalizeBlock inside the block's transaction so every
// validator holds an identical copy.
//
// The projection mirrors the ETL's entity_manager handlers
// (pkg/etl/processors/entity_manager) — validate, then apply — so that the
// consensus view of "who may act for whom" tracks what the ETL actually
// indexes. A transaction whose auth effects the ETL would reject is skipped
// here too; otherwise the projection would accumulate state from rejected
// transactions and diverge from the indexed world. Everything here must be a
// deterministic function of (committed auth state, transaction bytes): no
// clocks, no maps iterated for effect, reads by primary key only.
//
// Content semantics (metadata shape, genres, gating) are deliberately out of
// scope; they belong to the ETL, which can fix rules with a reindex. The one
// consequence: a transaction the ETL rejects on a content rule (say, a track
// create with no title) still projects here if its authorization is valid, so
// the projection treats such an entity id as taken. That conservative
// approximation is the price of keeping content rules out of consensus. This
// state exists so a later, height-gated upgrade can enforce authorization at
// consensus time.

// Entity types and actions the projection understands. These mirror the ETL's
// constants (pkg/etl/processors/entity_manager/handler.go); the ETL is a
// separate Go module, so they are restated here.
const (
	authEntityTypeUser         = "User"
	authEntityTypeTrack        = "Track"
	authEntityTypePlaylist     = "Playlist"
	authEntityTypeGrant        = "Grant"
	authEntityTypeDeveloperApp = "DeveloperApp"

	authActionCreate  = "Create"
	authActionUpdate  = "Update"
	authActionDelete  = "Delete"
	authActionApprove = "Approve"
	authActionReject  = "Reject"
)

// ID offsets mirror the ETL's; live creates below the offset are rejected
// there, so the projection skips them too. Migration replays predate the
// offsets and are exempt.
const (
	authPlaylistIDOffset = 400_000
	authTrackIDOffset    = 2_000_000
)

// authValidationError is a deterministic rule rejection, mirroring the ETL's
// ValidationError: the projection understood the transaction and refused it.
// Any other error out of the projection is a store failure — the caller could
// not tell — and the two must never be conflated.
type authValidationError struct {
	msg string
}

func (e *authValidationError) Error() string {
	return e.msg
}

func authValidationErrorf(format string, args ...any) error {
	return &authValidationError{msg: fmt.Sprintf(format, args...)}
}

// isAuthValidationError reports whether err is a rule rejection rather than a
// store failure.
func isAuthValidationError(err error) bool {
	var v *authValidationError
	return errors.As(err, &v)
}

// authTx is the projection's view of a ManageEntity transaction, common to
// the live and genesis-migration protos.
type authTx struct {
	UserID     int64
	EntityType string
	EntityID   int64
	Action     string
	Signer     string
	Migration  bool

	meta map[string]any
}

func authTxFromManageEntity(em *v1.ManageEntityLegacy) authTx {
	return authTx{
		UserID:     em.GetUserId(),
		EntityType: em.GetEntityType(),
		EntityID:   em.GetEntityId(),
		Action:     em.GetAction(),
		Signer:     em.GetSigner(),
		meta:       parseAuthMetadata(em.GetMetadata()),
	}
}

func authTxFromManageEntityMigration(em *v1.ManageEntityLegacyMigration) authTx {
	return authTx{
		UserID:     em.GetUserId(),
		EntityType: em.GetEntityType(),
		EntityID:   em.GetEntityId(),
		Action:     em.GetAction(),
		Signer:     em.GetSigner(),
		Migration:  true,
		meta:       parseAuthMetadata(em.GetMetadata()),
	}
}

// parseAuthMetadata mirrors the ETL's NewParams: parse the metadata JSON and
// unwrap the nested {"cid": ..., "data": {...}} envelope when present.
// Unparseable metadata resolves to nil, which reads as "field absent".
func parseAuthMetadata(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return nil
	}
	if data, ok := meta["data"].(map[string]any); ok {
		return data
	}
	return meta
}

func (t authTx) metaString(key string) string {
	v, ok := t.meta[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func (t authTx) metaInt64(key string) (int64, bool) {
	v, ok := t.meta[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}

func (t authTx) metaBoolOr(key string, def bool) bool {
	if v, ok := t.meta[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// Rows the projection reads and writes. Getters return (row, ok, err):
// ok=false means absent, and err is reserved for store failures.
type authUserRow struct {
	Wallet      string
	HandleLC    string
	Deactivated bool
}

type authGrantRow struct {
	// nil = pending user-to-user grant; developer-app grants are approved at
	// creation.
	Approved *bool
	Revoked  bool
}

type authAppRow struct {
	OwnerID int64
	Deleted bool
}

type authEntityRow struct {
	OwnerID int64
	Deleted bool
}

// authReader is the read half of the projection's view of the core_auth_*
// tables. Validation logic that only inspects state should ask for this
// rather than the full store.
type authReader interface {
	GetUser(ctx context.Context, userID int64) (authUserRow, bool, error)
	GetUserIDByWallet(ctx context.Context, wallet string) (int64, bool, error)
	WalletExists(ctx context.Context, wallet string) (bool, error)
	ActiveWalletExists(ctx context.Context, wallet string) (bool, error)
	HandleExists(ctx context.Context, handleLC string) (bool, error)
	GetGrant(ctx context.Context, granteeAddress string, userID int64) (authGrantRow, bool, error)
	GetApp(ctx context.Context, address string) (authAppRow, bool, error)
	GetEntity(ctx context.Context, entityType string, entityID int64) (authEntityRow, bool, error)
	IsCidClaimedByUser(ctx context.Context, cid string, userID int64) (bool, error)
	CidIsClaimed(ctx context.Context, cid string) (bool, error)
}

// authWriter is the write half.
type authWriter interface {
	InsertUser(ctx context.Context, userID int64, wallet, handleLC string, deactivated bool) error
	SetUserHandle(ctx context.Context, userID int64, handleLC string) error
	SetUserDeactivated(ctx context.Context, userID int64, deactivated bool) error
	UpsertGrant(ctx context.Context, granteeAddress string, userID int64, approved *bool, revoked bool) error
	UpsertApp(ctx context.Context, address string, ownerID int64) error
	SetAppDeleted(ctx context.Context, address string) error
	InsertEntity(ctx context.Context, entityType string, entityID, ownerID int64, deleted bool) error
	SetEntityDeleted(ctx context.Context, entityType string, entityID int64) error
	InsertCid(ctx context.Context, cid string, uploaderUserID int64, txHash string) error
}

// authStore is what the projection runs against: one state machine, two
// backing stores. dbAuthStore (auth_store_db.go) runs inside the block
// transaction and is the durable, sole writer of the tables; a later
// enforcement pass layers an ephemeral proposal-time store over the same
// interface. Tests use a map-backed one.
type authStore interface {
	authReader
	authWriter
}

// validateAuthSigner mirrors the ETL's ValidateSigner: the signer is the
// user's own wallet, or holds an active grant from that user — approved for
// user-to-user manager grants, implicit for developer apps — where the
// grantee is still a live app or active user. Returns an authValidationError
// when the signer is not authorized.
func validateAuthSigner(ctx context.Context, st authReader, userID int64, signer string) error {
	u, ok, err := st.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if !ok || u.Wallet == "" {
		return authValidationErrorf("user %d does not exist", userID)
	}
	if strings.EqualFold(u.Wallet, signer) {
		return nil
	}

	grantee := strings.ToLower(signer)
	if grantee == "" {
		return authValidationErrorf("no signer for user %d", userID)
	}
	g, ok, err := st.GetGrant(ctx, grantee, userID)
	if err != nil {
		return err
	}
	if !ok || g.Revoked {
		return authValidationErrorf("signer %s is not authorized for user %d", signer, userID)
	}

	app, appOk, err := st.GetApp(ctx, grantee)
	if err != nil {
		return err
	}
	isApp := appOk && !app.Deleted
	isUser, err := st.ActiveWalletExists(ctx, grantee)
	if err != nil {
		return err
	}
	if !isApp && !isUser {
		return authValidationErrorf("signer %s is no longer a valid developer app or active user", signer)
	}
	if !isApp && (g.Approved == nil || !*g.Approved) {
		return authValidationErrorf("signer %s grant for user %d is not approved", signer, userID)
	}
	return nil
}

// applyAuthProjection validates a ManageEntity transaction against the auth
// state and, when it passes, applies its authorization effects. It returns
// nil both when effects were applied and when the (entity type, action) pair
// is not auth-tracked at all — the projection has no opinion there. An
// authValidationError is the projection mirroring an ETL rejection; any other
// error is a store failure.
func applyAuthProjection(ctx context.Context, st authStore, tx authTx) error {
	entityType, ok := canonicalAuthEntityType(tx.EntityType)
	if !ok {
		return nil
	}
	action, ok := canonicalAuthAction(tx.Action)
	if !ok {
		return nil
	}

	switch {
	case entityType == authEntityTypeUser && action == authActionCreate:
		return projectUserCreate(ctx, st, tx)
	case entityType == authEntityTypeUser && action == authActionUpdate:
		return projectUserUpdate(ctx, st, tx)
	case (entityType == authEntityTypeTrack || entityType == authEntityTypePlaylist) && action == authActionCreate:
		if err := projectEntityCreate(ctx, st, tx, entityType); err != nil {
			return err
		}
		// Seeded here rather than at the FinalizeBlock call site so every
		// caller gets it — genesis-writer projects through this function too,
		// and cids left unseeded there make enforcement unactivatable. Runs
		// only after the entity itself projects: a track that was skipped must
		// not leave claims behind.
		return projectMigratedTrackCids(ctx, st, tx)
	case (entityType == authEntityTypeTrack || entityType == authEntityTypePlaylist) && action == authActionDelete:
		return projectEntityDelete(ctx, st, tx, entityType)
	case entityType == authEntityTypeGrant && action == authActionCreate:
		return projectGrantCreate(ctx, st, tx)
	case entityType == authEntityTypeGrant && action == authActionDelete:
		return projectGrantDelete(ctx, st, tx)
	case entityType == authEntityTypeGrant && (action == authActionApprove || action == authActionReject):
		return projectGrantApproveReject(ctx, st, tx, action == authActionApprove)
	case entityType == authEntityTypeDeveloperApp && action == authActionCreate:
		return projectAppCreate(ctx, st, tx)
	case entityType == authEntityTypeDeveloperApp && action == authActionDelete:
		return projectAppDelete(ctx, st, tx)
	}
	return nil
}

func canonicalAuthEntityType(s string) (string, bool) {
	for _, t := range []string{
		authEntityTypeUser, authEntityTypeTrack, authEntityTypePlaylist,
		authEntityTypeGrant, authEntityTypeDeveloperApp,
	} {
		if strings.EqualFold(s, t) {
			return t, true
		}
	}
	return "", false
}

func canonicalAuthAction(s string) (string, bool) {
	for _, a := range []string{
		authActionCreate, authActionUpdate, authActionDelete,
		authActionApprove, authActionReject,
	} {
		if strings.EqualFold(s, a) {
			return a, true
		}
	}
	return "", false
}

func projectUserCreate(ctx context.Context, st authStore, tx authTx) error {
	_, exists, err := st.GetUser(ctx, tx.UserID)
	if err != nil {
		return err
	}
	if exists {
		return authValidationErrorf("user %d already exists", tx.UserID)
	}
	wallet := strings.ToLower(tx.Signer)
	if wallet == "" {
		return authValidationErrorf("user %d create has no signer", tx.UserID)
	}
	handleLC := strings.ToLower(tx.metaString("handle"))

	// The genesis migration replays legacy state verbatim: it keeps only the
	// idempotency check, because the source data is the authority on wallet
	// and handle collisions. Live creates mirror the ETL's uniqueness rules.
	if !tx.Migration {
		used, err := st.WalletExists(ctx, wallet)
		if err != nil {
			return err
		}
		if used {
			return authValidationErrorf("wallet %s already in use", wallet)
		}
		app, ok, err := st.GetApp(ctx, wallet)
		if err != nil {
			return err
		}
		if ok && !app.Deleted {
			return authValidationErrorf("developer app %s cannot create user", wallet)
		}
		if handleLC != "" {
			taken, err := st.HandleExists(ctx, handleLC)
			if err != nil {
				return err
			}
			if taken {
				return authValidationErrorf("handle %q already exists", handleLC)
			}
		}
	}
	return st.InsertUser(ctx, tx.UserID, wallet, handleLC, tx.metaBoolOr("is_deactivated", false))
}

func projectUserUpdate(ctx context.Context, st authStore, tx authTx) error {
	if err := validateAuthSigner(ctx, st, tx.UserID, tx.Signer); err != nil {
		return err
	}
	if handle := tx.metaString("handle"); handle != "" {
		if err := st.SetUserHandle(ctx, tx.UserID, strings.ToLower(handle)); err != nil {
			return err
		}
	}
	if v, ok := tx.meta["is_deactivated"]; ok {
		if deactivated, ok := v.(bool); ok {
			if err := st.SetUserDeactivated(ctx, tx.UserID, deactivated); err != nil {
				return err
			}
		}
	}
	return nil
}

func projectEntityCreate(ctx context.Context, st authStore, tx authTx, entityType string) error {
	// Live creates respect the ID offsets; migration replays legacy IDs below
	// them. Tracks additionally pin metadata owner_id to the tx user id (both
	// live and migration), mirroring the ETL.
	if !tx.Migration {
		offset := int64(authPlaylistIDOffset)
		if entityType == authEntityTypeTrack {
			offset = authTrackIDOffset
		}
		if tx.EntityID < offset {
			return authValidationErrorf("%s id %d below offset %d", entityType, tx.EntityID, offset)
		}
	}
	if entityType == authEntityTypeTrack {
		ownerID, ok := tx.metaInt64("owner_id")
		if !ok || ownerID != tx.UserID {
			return authValidationErrorf("track %d metadata owner_id missing or mismatched", tx.EntityID)
		}
	}
	if err := validateAuthSigner(ctx, st, tx.UserID, tx.Signer); err != nil {
		return err
	}
	_, exists, err := st.GetEntity(ctx, entityType, tx.EntityID)
	if err != nil {
		return err
	}
	if exists {
		return authValidationErrorf("%s %d already exists", entityType, tx.EntityID)
	}
	deleted := tx.Migration && tx.metaBoolOr("is_delete", false)
	return st.InsertEntity(ctx, entityType, tx.EntityID, tx.UserID, deleted)
}

func projectEntityDelete(ctx context.Context, st authStore, tx authTx, entityType string) error {
	if err := validateAuthSigner(ctx, st, tx.UserID, tx.Signer); err != nil {
		return err
	}
	existing, ok, err := st.GetEntity(ctx, entityType, tx.EntityID)
	if err != nil {
		return err
	}
	if !ok {
		return authValidationErrorf("%s %d does not exist", entityType, tx.EntityID)
	}
	if existing.OwnerID != tx.UserID {
		return authValidationErrorf("%s %d does not belong to user %d", entityType, tx.EntityID, tx.UserID)
	}
	return st.SetEntityDeleted(ctx, entityType, tx.EntityID)
}

func projectGrantCreate(ctx context.Context, st authStore, tx authTx) error {
	grantee := strings.ToLower(tx.metaString("grantee_address"))
	if grantee == "" {
		return authValidationErrorf("grantee_address is required for grant creation")
	}
	if err := validateAuthSigner(ctx, st, tx.UserID, tx.Signer); err != nil {
		return err
	}
	app, appOk, err := st.GetApp(ctx, grantee)
	if err != nil {
		return err
	}
	isApp := appOk && !app.Deleted
	isUser, err := st.WalletExists(ctx, grantee)
	if err != nil {
		return err
	}
	if !isApp && !isUser {
		return authValidationErrorf("grantee %s is not a developer app or user wallet", grantee)
	}
	existing, ok, err := st.GetGrant(ctx, grantee, tx.UserID)
	if err != nil {
		return err
	}
	if ok && !existing.Revoked {
		return authValidationErrorf("active grant already exists for grantee %s from user %d", grantee, tx.UserID)
	}
	// Developer-app grants are approved at creation; user-to-user grants
	// start pending until the grantee approves.
	var approved *bool
	if isApp {
		t := true
		approved = &t
	}
	return st.UpsertGrant(ctx, grantee, tx.UserID, approved, false)
}

func projectGrantDelete(ctx context.Context, st authStore, tx authTx) error {
	grantee := strings.ToLower(tx.metaString("grantee_address"))
	if grantee == "" {
		return authValidationErrorf("grantee_address is required for grant revoke")
	}
	existing, ok, err := st.GetGrant(ctx, grantee, tx.UserID)
	if err != nil {
		return err
	}
	if !ok || existing.Revoked {
		return authValidationErrorf("no active grant for grantee %s from user %d", grantee, tx.UserID)
	}
	// The grantor revokes, or — for a user-to-user grant — the grantee
	// revokes their own management relationship.
	sigErr := validateAuthSigner(ctx, st, tx.UserID, tx.Signer)
	if sigErr != nil && !isAuthValidationError(sigErr) {
		return sigErr
	}
	if sigErr != nil {
		granteeUserID, isUserGrant, err := st.GetUserIDByWallet(ctx, grantee)
		if err != nil {
			return err
		}
		authorized := false
		if isUserGrant {
			granteeErr := validateAuthSigner(ctx, st, granteeUserID, tx.Signer)
			if granteeErr != nil && !isAuthValidationError(granteeErr) {
				return granteeErr
			}
			authorized = granteeErr == nil
		}
		if !authorized {
			return authValidationErrorf("signer %s is not authorized to revoke the grant for grantee %s from user %d", tx.Signer, grantee, tx.UserID)
		}
	}
	return st.UpsertGrant(ctx, grantee, tx.UserID, existing.Approved, true)
}

func projectGrantApproveReject(ctx context.Context, st authStore, tx authTx, approve bool) error {
	grantee := strings.ToLower(tx.metaString("grantee_address"))
	if grantee == "" {
		return authValidationErrorf("grantee_address is required for grant approve/reject")
	}
	if err := validateAuthSigner(ctx, st, tx.UserID, tx.Signer); err != nil {
		return err
	}
	grantor, present := tx.metaInt64("grantor_user_id")
	if !present {
		return authValidationErrorf("grantor_user_id is required for grant approve/reject")
	}
	existing, ok, err := st.GetGrant(ctx, grantee, grantor)
	if err != nil {
		return err
	}
	if !ok {
		return authValidationErrorf("grant not found for grantee %s from user %d", grantee, grantor)
	}
	if existing.Revoked {
		return authValidationErrorf("grant is already revoked")
	}
	if existing.Approved != nil && *existing.Approved {
		return authValidationErrorf("grant is already approved")
	}
	return st.UpsertGrant(ctx, grantee, grantor, &approve, !approve)
}

func projectAppCreate(ctx context.Context, st authStore, tx authTx) error {
	if err := validateAuthSigner(ctx, st, tx.UserID, tx.Signer); err != nil {
		return err
	}
	address := strings.ToLower(tx.metaString("address"))
	if address == "" {
		return authValidationErrorf("address is required for developer app")
	}
	existing, ok, err := st.GetApp(ctx, address)
	if err != nil {
		return err
	}
	if ok && !existing.Deleted {
		return authValidationErrorf("developer app %s already exists", address)
	}
	walletUsed, err := st.WalletExists(ctx, address)
	if err != nil {
		return err
	}
	if walletUsed {
		return authValidationErrorf("address %s is already a user wallet", address)
	}
	return st.UpsertApp(ctx, address, tx.UserID)
}

func projectAppDelete(ctx context.Context, st authStore, tx authTx) error {
	if err := validateAuthSigner(ctx, st, tx.UserID, tx.Signer); err != nil {
		return err
	}
	address := strings.ToLower(tx.metaString("address"))
	if address == "" {
		return authValidationErrorf("address is required for developer app delete")
	}
	existing, ok, err := st.GetApp(ctx, address)
	if err != nil {
		return err
	}
	if !ok || existing.Deleted {
		return authValidationErrorf("developer app %s does not exist", address)
	}
	if existing.OwnerID != tx.UserID {
		return authValidationErrorf("developer app %s does not belong to user %d", address, tx.UserID)
	}
	return st.SetAppDeleted(ctx, address)
}

// ProjectMigrationAuthState applies a genesis-migration ManageEntity
// transaction's authorization effects to the core_auth_* tables, using the
// caller's queries handle so the writes join whatever transaction it owns.
//
// It exists for genesis-writer, which inserts blocks straight into postgres
// without going through consensus: FinalizeBlock never executes those
// transactions, so unless the writer projects the auth state itself the new
// chain starts with the core_auth_* tables empty and enforcement rejects
// everything. It deliberately reuses applyAuthProjection so the writer and
// FinalizeBlock cannot disagree.
//
// skipped reports that the projection understood the transaction and declined
// it, with reason describing why. A migration replays state the source system
// already accepted, so any skip is a defect the caller should surface.
func ProjectMigrationAuthState(ctx context.Context, q *db.Queries, me *v1.ManageEntityLegacyMigration) (skipped bool, reason string, err error) {
	err = applyAuthProjection(ctx, &dbAuthStore{q: q}, authTxFromManageEntityMigration(me))
	switch {
	case err == nil:
		return false, "", nil
	case isAuthValidationError(err):
		return true, err.Error(), nil
	default:
		return false, "", err
	}
}
