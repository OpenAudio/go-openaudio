package entity_manager

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTrackCollaboratorApprove_TxType(t *testing.T) {
	h := TrackCollaboratorApprove()
	if h.EntityType() != EntityTypeTrackCollaborator {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeTrackCollaborator)
	}
	if h.Action() != ActionApprove {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionApprove)
	}
}

func TestTrackCollaboratorReject_TxType(t *testing.T) {
	h := TrackCollaboratorReject()
	if h.EntityType() != EntityTypeTrackCollaborator {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeTrackCollaborator)
	}
	if h.Action() != ActionReject {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionReject)
	}
}

// getCollaboratorUserIDs is pure (no DB), so this runs without ETL_TEST_DB_URL.
func TestGetCollaboratorUserIDs(t *testing.T) {
	owner := int64(UserIDOffset + 1)
	tests := []struct {
		name string
		meta map[string]any
		want []int64
	}{
		{"nil metadata", nil, nil},
		{"absent key", map[string]any{"title": "x"}, nil},
		{"not an array", map[string]any{"collaborators": "nope"}, nil},
		{
			"numeric ids, excludes owner + dedups",
			map[string]any{"collaborators": []any{float64(owner), float64(UserIDOffset + 2), float64(UserIDOffset + 2), float64(UserIDOffset + 3)}},
			[]int64{UserIDOffset + 2, UserIDOffset + 3},
		},
		{
			"object entries with user_id",
			map[string]any{"collaborators": []any{
				map[string]any{"user_id": float64(UserIDOffset + 5)},
				map[string]any{"user_id": float64(UserIDOffset + 6)},
			}},
			[]int64{UserIDOffset + 5, UserIDOffset + 6},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getCollaboratorUserIDs(tc.meta, owner)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// statusOf returns the stored status, or "" if absent.
func statusOf(t *testing.T, pool *pgxpool.Pool, trackID, collaboratorUserID int64) string {
	t.Helper()
	var status string
	err := pool.QueryRow(context.Background(),
		"SELECT status FROM track_collaborators WHERE track_id = $1 AND collaborator_user_id = $2",
		trackID, collaboratorUserID).Scan(&status)
	if err != nil {
		return ""
	}
	return status
}

func TestTrackCollaborators_CreateInsertsPending(t *testing.T) {
	pool := setupTestDB(t)
	owner := int64(UserIDOffset + 1)
	c1 := int64(UserIDOffset + 2)
	c2 := int64(UserIDOffset + 3)
	seedUser(t, pool, owner, "0xowner", "owner")
	seedUser(t, pool, c1, "0xc1", "collab1")
	seedUser(t, pool, c2, "0xc2", "collab2")

	tid := int64(TrackIDOffset + 1)
	meta := fmt.Sprintf(`{"owner_id":%d,"title":"Song","collaborators":[%d,%d]}`, owner, c1, c2)
	mustHandle(t, TrackCreate(), buildParams(t, pool, EntityTypeTrack, ActionCreate, owner, tid, "0xowner", meta))

	if got := statusOf(t, pool, tid, c1); got != "pending" {
		t.Errorf("c1 status = %q, want pending", got)
	}
	if got := statusOf(t, pool, tid, c2); got != "pending" {
		t.Errorf("c2 status = %q, want pending", got)
	}
	// Owner is never recorded as a collaborator of their own track.
	if got := statusOf(t, pool, tid, owner); got != "" {
		t.Errorf("owner should not be a collaborator, got %q", got)
	}
}

func TestTrackCollaborator_ApproveAndReject(t *testing.T) {
	pool := setupTestDB(t)
	owner := int64(UserIDOffset + 1)
	c1 := int64(UserIDOffset + 2)
	c2 := int64(UserIDOffset + 3)
	seedUser(t, pool, owner, "0xowner", "owner")
	seedUser(t, pool, c1, "0xc1", "collab1")
	seedUser(t, pool, c2, "0xc2", "collab2")

	tid := int64(TrackIDOffset + 1)
	meta := fmt.Sprintf(`{"owner_id":%d,"title":"Song","collaborators":[%d,%d]}`, owner, c1, c2)
	mustHandle(t, TrackCreate(), buildParams(t, pool, EntityTypeTrack, ActionCreate, owner, tid, "0xowner", meta))

	// c1 accepts.
	mustHandle(t, TrackCollaboratorApprove(),
		buildParams(t, pool, EntityTypeTrackCollaborator, ActionApprove, c1, tid, "0xc1", ""))
	if got := statusOf(t, pool, tid, c1); got != "accepted" {
		t.Errorf("c1 status = %q, want accepted", got)
	}

	// c2 declines.
	mustHandle(t, TrackCollaboratorReject(),
		buildParams(t, pool, EntityTypeTrackCollaborator, ActionReject, c2, tid, "0xc2", ""))
	if got := statusOf(t, pool, tid, c2); got != "rejected" {
		t.Errorf("c2 status = %q, want rejected", got)
	}
}

func TestTrackCollaborator_ApproveWithoutInviteRejected(t *testing.T) {
	pool := setupTestDB(t)
	owner := int64(UserIDOffset + 1)
	stranger := int64(UserIDOffset + 9)
	seedUser(t, pool, owner, "0xowner", "owner")
	seedUser(t, pool, stranger, "0xstranger", "stranger")

	tid := int64(TrackIDOffset + 1)
	meta := fmt.Sprintf(`{"owner_id":%d,"title":"Song"}`, owner)
	mustHandle(t, TrackCreate(), buildParams(t, pool, EntityTypeTrack, ActionCreate, owner, tid, "0xowner", meta))

	mustReject(t, TrackCollaboratorApprove(),
		buildParams(t, pool, EntityTypeTrackCollaborator, ActionApprove, stranger, tid, "0xstranger", ""),
		"no collaborator invite")
}

func TestTrackCollaborator_AlreadyAcceptedNotPending(t *testing.T) {
	pool := setupTestDB(t)
	owner := int64(UserIDOffset + 1)
	c1 := int64(UserIDOffset + 2)
	seedUser(t, pool, owner, "0xowner", "owner")
	seedUser(t, pool, c1, "0xc1", "collab1")

	tid := int64(TrackIDOffset + 1)
	meta := fmt.Sprintf(`{"owner_id":%d,"title":"Song","collaborators":[%d]}`, owner, c1)
	mustHandle(t, TrackCreate(), buildParams(t, pool, EntityTypeTrack, ActionCreate, owner, tid, "0xowner", meta))

	mustHandle(t, TrackCollaboratorApprove(),
		buildParams(t, pool, EntityTypeTrackCollaborator, ActionApprove, c1, tid, "0xc1", ""))
	// A second approve must fail — the invite is no longer pending.
	mustReject(t, TrackCollaboratorApprove(),
		buildParams(t, pool, EntityTypeTrackCollaborator, ActionApprove, c1, tid, "0xc1", ""),
		"not pending")
}

func TestTrackCollaborators_UpdateReconciles(t *testing.T) {
	pool := setupTestDB(t)
	owner := int64(UserIDOffset + 1)
	c1 := int64(UserIDOffset + 2)
	c2 := int64(UserIDOffset + 3)
	seedUser(t, pool, owner, "0xowner", "owner")
	seedUser(t, pool, c1, "0xc1", "collab1")
	seedUser(t, pool, c2, "0xc2", "collab2")

	tid := int64(TrackIDOffset + 1)
	meta := fmt.Sprintf(`{"owner_id":%d,"title":"Song","collaborators":[%d,%d]}`, owner, c1, c2)
	mustHandle(t, TrackCreate(), buildParams(t, pool, EntityTypeTrack, ActionCreate, owner, tid, "0xowner", meta))

	// c1 accepts.
	mustHandle(t, TrackCollaboratorApprove(),
		buildParams(t, pool, EntityTypeTrackCollaborator, ActionApprove, c1, tid, "0xc1", ""))

	// Owner edits the track, dropping c2 and keeping c1.
	updateMeta := fmt.Sprintf(`{"owner_id":%d,"collaborators":[%d]}`, owner, c1)
	mustHandle(t, TrackUpdate(), buildParams(t, pool, EntityTypeTrack, ActionUpdate, owner, tid, "0xowner", updateMeta))

	// c1's accepted status is preserved; c2 is removed.
	if got := statusOf(t, pool, tid, c1); got != "accepted" {
		t.Errorf("c1 status = %q, want accepted (preserved)", got)
	}
	if got := statusOf(t, pool, tid, c2); got != "" {
		t.Errorf("c2 should be removed, got %q", got)
	}
}

func TestTrackCollaborators_UpdateWithoutKeyLeavesRowsUntouched(t *testing.T) {
	pool := setupTestDB(t)
	owner := int64(UserIDOffset + 1)
	c1 := int64(UserIDOffset + 2)
	seedUser(t, pool, owner, "0xowner", "owner")
	seedUser(t, pool, c1, "0xc1", "collab1")

	tid := int64(TrackIDOffset + 1)
	meta := fmt.Sprintf(`{"owner_id":%d,"title":"Song","collaborators":[%d]}`, owner, c1)
	mustHandle(t, TrackCreate(), buildParams(t, pool, EntityTypeTrack, ActionCreate, owner, tid, "0xowner", meta))

	// Unrelated edit (no collaborators key) must not clear collaborators.
	updateMeta := fmt.Sprintf(`{"owner_id":%d,"mood":"Energizing"}`, owner)
	mustHandle(t, TrackUpdate(), buildParams(t, pool, EntityTypeTrack, ActionUpdate, owner, tid, "0xowner", updateMeta))

	if got := statusOf(t, pool, tid, c1); got != "pending" {
		t.Errorf("c1 status = %q, want pending (untouched)", got)
	}
}
