package entity_manager

import (
	"context"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
)

// A collaborator accepts or declines a track invite via a TrackCollaborator
// Approve/Reject ManageEntity tx: entity_id = track_id, and the signing
// user_id is the collaborator. Mirrors the Grant Approve/Reject flow — only a
// 'pending' invite can transition, and only by the invited collaborator.

type trackCollaboratorApproveHandler struct{}

func (h *trackCollaboratorApproveHandler) EntityType() string { return EntityTypeTrackCollaborator }
func (h *trackCollaboratorApproveHandler) Action() string     { return ActionApprove }

func (h *trackCollaboratorApproveHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateTrackCollaboratorPending(ctx, params); err != nil {
		return err
	}
	return setTrackCollaboratorStatus(ctx, params, "accepted")
}

type trackCollaboratorRejectHandler struct{}

func (h *trackCollaboratorRejectHandler) EntityType() string { return EntityTypeTrackCollaborator }
func (h *trackCollaboratorRejectHandler) Action() string     { return ActionReject }

func (h *trackCollaboratorRejectHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateTrackCollaboratorPending(ctx, params); err != nil {
		return err
	}
	return setTrackCollaboratorStatus(ctx, params, "rejected")
}

func validateTrackCollaboratorPending(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	status, err := getTrackCollaboratorStatus(ctx, params.DBTX, params.EntityID, params.UserID)
	if err != nil {
		return NewValidationError("no collaborator invite for track %d and user %d", params.EntityID, params.UserID)
	}
	if status != "pending" {
		return NewValidationError("collaborator invite for track %d and user %d is not pending (status=%s)", params.EntityID, params.UserID, status)
	}
	return nil
}

func getTrackCollaboratorStatus(ctx context.Context, dbtx db.DBTX, trackID, collaboratorUserID int64) (string, error) {
	var status string
	err := dbtx.QueryRow(ctx,
		"SELECT status FROM track_collaborators WHERE track_id = $1 AND collaborator_user_id = $2",
		trackID, collaboratorUserID).Scan(&status)
	return status, err
}

func setTrackCollaboratorStatus(ctx context.Context, params *Params, status string) error {
	_, err := params.DBTX.Exec(ctx, `
		UPDATE track_collaborators
		SET status = $3, updated_at = $4
		WHERE track_id = $1 AND collaborator_user_id = $2
	`, params.EntityID, params.UserID, status, params.BlockTime)
	return err
}

// TrackCollaboratorApprove returns the TrackCollaborator Approve handler.
func TrackCollaboratorApprove() Handler { return &trackCollaboratorApproveHandler{} }

// TrackCollaboratorReject returns the TrackCollaborator Reject handler.
func TrackCollaboratorReject() Handler { return &trackCollaboratorRejectHandler{} }
