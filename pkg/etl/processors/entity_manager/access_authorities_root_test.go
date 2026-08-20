package entity_manager

import (
	"context"
	"fmt"
	"testing"
)

// access_authorities must be projected from the same field core enforces.
// core's finalizeManageEntity reads the root of the envelope and turns those
// wallets into management_keys; if this column were built from the data
// payload instead, a client could put one list at the root and another in the
// payload and the enforced state would not match the queryable one.
func TestAccessAuthorities_RootWinsOverPayload(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 661)
	seedUser(t, pool, uid, "0xrootauth", "rootauth")

	rootAddr := "0xAAAA000000000000000000000000000000000001"
	payloadAddr := "0xBBBB000000000000000000000000000000000002"

	cases := []struct {
		name     string
		trackID  int64
		metadata string
		want     []string
	}{
		{
			name:    "root wins when both are present",
			trackID: int64(TrackIDOffset + 661),
			metadata: fmt.Sprintf(`{"cid":"c1","access_authorities":["%s"],
				"data":{"title":"t","owner_id":%d,"track_cid":"tc","access_authorities":["%s"]}}`,
				rootAddr, uid, payloadAddr),
			want: []string{rootAddr},
		},
		{
			name:    "root alone is honoured",
			trackID: int64(TrackIDOffset + 662),
			metadata: fmt.Sprintf(`{"cid":"c1","access_authorities":["%s"],
				"data":{"title":"t","owner_id":%d,"track_cid":"tc"}}`, rootAddr, uid),
			want: []string{rootAddr},
		},
		{
			// Clients predating root support send it only in the payload.
			name:    "payload is still honoured when the root has none",
			trackID: int64(TrackIDOffset + 663),
			metadata: fmt.Sprintf(`{"cid":"c1","data":{"title":"t","owner_id":%d,"track_cid":"tc","access_authorities":["%s"]}}`,
				uid, payloadAddr),
			want: []string{payloadAddr},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mustHandle(t, TrackCreate(),
				buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, c.trackID, "0xRootAuth", c.metadata))

			var got []string
			if err := pool.QueryRow(context.Background(),
				"SELECT access_authorities FROM tracks WHERE track_id = $1 AND is_current = true",
				c.trackID).Scan(&got); err != nil {
				t.Fatalf("query: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("access_authorities = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("access_authorities[%d] = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// An explicit null at the root clears the column, the same as it does in the
// payload -- present-but-null means "unset this", not "leave it alone".
func TestAccessAuthorities_ExplicitRootNullClears(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 664)
	trackID := int64(TrackIDOffset + 664)
	seedUser(t, pool, uid, "0xrootnull", "rootnull")

	meta := fmt.Sprintf(`{"cid":"c1","access_authorities":null,
		"data":{"title":"t","owner_id":%d,"track_cid":"tc","access_authorities":["0xCCCC000000000000000000000000000000000003"]}}`, uid)
	mustHandle(t, TrackCreate(),
		buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, trackID, "0xRootNull", meta))

	var got []string
	if err := pool.QueryRow(context.Background(),
		"SELECT access_authorities FROM tracks WHERE track_id = $1 AND is_current = true", trackID).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("access_authorities = %v, want NULL -- an explicit root null must clear, not fall back to the payload", got)
	}
}
