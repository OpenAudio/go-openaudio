package entity_manager

import (
	"context"
	"database/sql"
	"testing"
)

func TestTrackCreate_RejectsInvalidGating(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xgateowner", "gateowner")

	tests := []struct {
		name    string
		meta    string
		wantErr string
	}{
		{
			name:    "stream gated without download gated",
			meta:    `{"owner_id":3000001,"title":"T","is_stream_gated":true,"stream_conditions":{"tip_user_id":1}}`,
			wantErr: "must also be download gated",
		},
		{
			name:    "stream gated no conditions",
			meta:    `{"owner_id":3000001,"title":"T","is_stream_gated":true,"is_download_gated":true}`,
			wantErr: "must have stream_conditions",
		},
		{
			name:    "stream/download conditions mismatch",
			meta:    `{"owner_id":3000001,"title":"T","is_stream_gated":true,"is_download_gated":true,"stream_conditions":{"tip_user_id":1},"download_conditions":{"follow_user_id":2}}`,
			wantErr: "must match",
		},
		{
			name:    "download gated no conditions",
			meta:    `{"owner_id":3000001,"title":"T","is_download_gated":true}`,
			wantErr: "must have download_conditions",
		},
		{
			name:    "stem cannot be gated",
			meta:    `{"owner_id":3000001,"title":"T","is_stream_gated":true,"is_download_gated":true,"stream_conditions":{"tip_user_id":1},"download_conditions":{"tip_user_id":1},"stem_of":{"parent_track_id":123}}`,
			wantErr: "stem tracks cannot",
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tid := int64(TrackIDOffset + 500 + int64(i))
			params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xGateOwner", tt.meta)
			mustReject(t, TrackCreate(), params, tt.wantErr)
		})
	}
}

func TestTrackCreate_ValidGating(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xgateowner2", "gateowner2")

	tests := []struct {
		name string
		meta string
	}{
		{
			name: "valid stream+download gated",
			meta: `{"owner_id":3000001,"title":"Gated Track","is_stream_gated":true,"is_download_gated":true,"stream_conditions":{"tip_user_id":1},"download_conditions":{"tip_user_id":1}}`,
		},
		{
			name: "valid download-only gated",
			meta: `{"owner_id":3000001,"title":"DL Gated","is_download_gated":true,"download_conditions":{"follow_user_id":1}}`,
		},
		{
			name: "valid USDC purchase",
			meta: `{"owner_id":3000001,"title":"Paid Track","is_stream_gated":true,"is_download_gated":true,"stream_conditions":{"usdc_purchase":{"price":100,"splits":[{"user_id":1,"percentage":100}]}},"download_conditions":{"usdc_purchase":{"price":100,"splits":[{"user_id":1,"percentage":100}]}}}`,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tid := int64(TrackIDOffset + 600 + int64(i))
			params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xGateOwner2", tt.meta)
			mustHandle(t, TrackCreate(), params)
		})
	}
}

func TestTrackCreate_WithReleaseDate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 700)
	seedUser(t, pool, uid, "0xrdowner", "rdowner")

	meta := `{"owner_id":3000001,"title":"Future Release","release_date":"2025-06-15T00:00:00Z","is_scheduled_release":true}`
	params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xRdOwner", meta)
	mustHandle(t, TrackCreate(), params)

	var releaseDate sql.NullTime
	var isScheduled bool
	err := pool.QueryRow(context.Background(),
		"SELECT release_date, is_scheduled_release FROM tracks WHERE track_id = $1 AND is_current = true", tid).Scan(&releaseDate, &isScheduled)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !releaseDate.Valid {
		t.Error("expected release_date to be set")
	}
	if !isScheduled {
		t.Error("expected is_scheduled_release = true")
	}
}

func TestTrackUpdate_GatingRejection(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 800)
	seedUser(t, pool, uid, "0xupdgateowner", "updgateowner")
	seedTrackFull(t, pool, tid, uid, "Original Title")

	meta := `{"is_stream_gated":true,"stream_conditions":{"tip_user_id":1}}`
	params := buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xUpdGateOwner", meta)
	mustReject(t, TrackUpdate(), params, "must also be download gated")
}
