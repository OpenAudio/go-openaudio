package main

import (
	"context"
	"fmt"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
)

// loadTrackCollaborators pre-loads all non-rejected collaborator user IDs
// grouped by track_id. The returned map is used during Track:Create to include
// collaborator IDs in the track metadata, which causes the ETL to create
// pending invites automatically.
func (w *Writer) loadTrackCollaborators(ctx context.Context) (map[int64][]int64, error) {
	rows, err := w.srcDB.Query(ctx,
		`SELECT track_id, collaborator_user_id
		FROM track_collaborators
		WHERE status IN ('pending', 'accepted')
		ORDER BY track_id, collaborator_user_id`)
	if err != nil {
		return nil, fmt.Errorf("query track collaborators: %w", err)
	}
	defer rows.Close()

	m := make(map[int64][]int64)
	for rows.Next() {
		var trackID, userID int64
		if err := rows.Scan(&trackID, &userID); err != nil {
			return nil, fmt.Errorf("scan track collaborator: %w", err)
		}
		m[trackID] = append(m[trackID], userID)
	}
	return m, rows.Err()
}

// writeTrackCollaboratorApprovals emits TrackCollaborator:Approve transactions
// for every accepted collaborator. These must run after Track:Create (which
// establishes the pending invites via the collaborators metadata field).
func (w *Writer) writeTrackCollaboratorApprovals(ctx context.Context) error {
	return processBatched(ctx, w, "track collaborator approvals",
		`SELECT count(*) FROM track_collaborators WHERE status = 'accepted'`,
		`SELECT tc.track_id, tc.collaborator_user_id, COALESCE(LOWER(u.wallet), '')
		FROM track_collaborators tc
		LEFT JOIN (
			-- At most one row per user: the source contains a few user_ids with
			-- more than one is_current row, and joining users directly would
			-- duplicate every entity those users own.
			SELECT DISTINCT ON (user_id) user_id, wallet FROM users
			WHERE is_current = true ORDER BY user_id, blocknumber DESC NULLS LAST
		) u ON u.user_id = tc.collaborator_user_id
		WHERE tc.status = 'accepted'
		ORDER BY tc.track_id, tc.collaborator_user_id`,
		func(rows pgx.Rows) (sourceCollaboratorApproval, error) {
			var c sourceCollaboratorApproval
			err := rows.Scan(&c.TrackID, &c.UserID, &c.UserWallet)
			return c, err
		},
		func(ctx context.Context, c sourceCollaboratorApproval) error {
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     c.UserID,
				EntityType: "TrackCollaborator",
				EntityId:   c.TrackID,
				Action:     "Approve",
				Metadata:   "",
			}, c.UserWallet)
		},
	)
}

type sourceCollaboratorApproval struct {
	TrackID    int64
	UserID     int64
	UserWallet string
}
