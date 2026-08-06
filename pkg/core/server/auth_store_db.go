package server

import (
	"context"
	"errors"

	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// dbAuthStore is the durable implementation of authStore (auth_state.go):
// FinalizeBlock points the auth projection at it, bound to the block's pg
// transaction, and it is the only path that writes the core_auth_* tables.

type dbAuthStore struct {
	q *db.Queries
}

func (s *dbAuthStore) GetUser(ctx context.Context, userID int64) (authUserRow, bool, error) {
	row, err := s.q.GetAuthUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return authUserRow{}, false, nil
	}
	if err != nil {
		return authUserRow{}, false, err
	}
	return authUserRow{
		Wallet:      row.Wallet,
		HandleLC:    row.HandleLc.String,
		Deactivated: row.IsDeactivated,
	}, true, nil
}

func (s *dbAuthStore) GetUserIDByWallet(ctx context.Context, wallet string) (int64, bool, error) {
	id, err := s.q.GetAuthUserIDByWallet(ctx, wallet)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func (s *dbAuthStore) WalletExists(ctx context.Context, wallet string) (bool, error) {
	return s.q.AuthWalletExists(ctx, wallet)
}

func (s *dbAuthStore) ActiveWalletExists(ctx context.Context, wallet string) (bool, error) {
	return s.q.AuthActiveWalletExists(ctx, wallet)
}

func (s *dbAuthStore) HandleExists(ctx context.Context, handleLC string) (bool, error) {
	return s.q.AuthHandleExists(ctx, pgtype.Text{String: handleLC, Valid: true})
}

func (s *dbAuthStore) GetGrant(ctx context.Context, granteeAddress string, userID int64) (authGrantRow, bool, error) {
	row, err := s.q.GetAuthGrant(ctx, db.GetAuthGrantParams{GranteeAddress: granteeAddress, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return authGrantRow{}, false, nil
	}
	if err != nil {
		return authGrantRow{}, false, err
	}
	g := authGrantRow{Revoked: row.IsRevoked}
	if row.IsApproved.Valid {
		approved := row.IsApproved.Bool
		g.Approved = &approved
	}
	return g, true, nil
}

func (s *dbAuthStore) GetApp(ctx context.Context, address string) (authAppRow, bool, error) {
	row, err := s.q.GetAuthDeveloperApp(ctx, address)
	if errors.Is(err, pgx.ErrNoRows) {
		return authAppRow{}, false, nil
	}
	if err != nil {
		return authAppRow{}, false, err
	}
	return authAppRow{OwnerID: row.UserID, Deleted: row.IsDeleted}, true, nil
}

func (s *dbAuthStore) GetEntity(ctx context.Context, entityType string, entityID int64) (authEntityRow, bool, error) {
	row, err := s.q.GetAuthEntity(ctx, db.GetAuthEntityParams{EntityType: entityType, EntityID: entityID})
	if errors.Is(err, pgx.ErrNoRows) {
		return authEntityRow{}, false, nil
	}
	if err != nil {
		return authEntityRow{}, false, err
	}
	return authEntityRow{OwnerID: row.OwnerUserID, Deleted: row.IsDeleted}, true, nil
}

func (s *dbAuthStore) InsertUser(ctx context.Context, userID int64, wallet, handleLC string, deactivated bool) error {
	handle := pgtype.Text{}
	if handleLC != "" {
		handle = pgtype.Text{String: handleLC, Valid: true}
	}
	return s.q.InsertAuthUser(ctx, db.InsertAuthUserParams{
		UserID:        userID,
		Wallet:        wallet,
		HandleLc:      handle,
		IsDeactivated: deactivated,
	})
}

func (s *dbAuthStore) SetUserHandle(ctx context.Context, userID int64, handleLC string) error {
	return s.q.SetAuthUserHandle(ctx, db.SetAuthUserHandleParams{
		UserID:   userID,
		HandleLc: pgtype.Text{String: handleLC, Valid: true},
	})
}

func (s *dbAuthStore) SetUserDeactivated(ctx context.Context, userID int64, deactivated bool) error {
	return s.q.SetAuthUserDeactivated(ctx, db.SetAuthUserDeactivatedParams{
		UserID:        userID,
		IsDeactivated: deactivated,
	})
}

func (s *dbAuthStore) UpsertGrant(ctx context.Context, granteeAddress string, userID int64, approved *bool, revoked bool) error {
	isApproved := pgtype.Bool{}
	if approved != nil {
		isApproved = pgtype.Bool{Bool: *approved, Valid: true}
	}
	return s.q.UpsertAuthGrant(ctx, db.UpsertAuthGrantParams{
		GranteeAddress: granteeAddress,
		UserID:         userID,
		IsApproved:     isApproved,
		IsRevoked:      revoked,
	})
}

func (s *dbAuthStore) UpsertApp(ctx context.Context, address string, ownerID int64) error {
	return s.q.UpsertAuthDeveloperApp(ctx, db.UpsertAuthDeveloperAppParams{
		Address: address,
		UserID:  ownerID,
	})
}

func (s *dbAuthStore) SetAppDeleted(ctx context.Context, address string) error {
	return s.q.SetAuthDeveloperAppDeleted(ctx, address)
}

func (s *dbAuthStore) InsertEntity(ctx context.Context, entityType string, entityID, ownerID int64, deleted bool) error {
	return s.q.InsertAuthEntity(ctx, db.InsertAuthEntityParams{
		EntityType:  entityType,
		EntityID:    entityID,
		OwnerUserID: ownerID,
		IsDeleted:   deleted,
	})
}

func (s *dbAuthStore) SetEntityDeleted(ctx context.Context, entityType string, entityID int64) error {
	return s.q.SetAuthEntityDeleted(ctx, db.SetAuthEntityDeletedParams{
		EntityType: entityType,
		EntityID:   entityID,
	})
}
