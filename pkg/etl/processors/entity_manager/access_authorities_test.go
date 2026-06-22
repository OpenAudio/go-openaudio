package entity_manager

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNormalizeAllowedAPIKeys_LowercasesValues(t *testing.T) {
	meta := map[string]any{}
	_ = json.Unmarshal([]byte(`{"allowed_api_keys":["KEY-A","Key-B","key-c"]}`), &meta)
	vals, present, isNull := normalizeAllowedAPIKeys(meta)
	if !present || isNull {
		t.Fatal("expected present, non-null")
	}
	want := []string{"key-a", "key-b", "key-c"}
	if len(vals) != len(want) {
		t.Fatalf("got %v, want %v", vals, want)
	}
	for i, v := range vals {
		if v != want[i] {
			t.Errorf("[%d] = %q, want %q", i, v, want[i])
		}
	}
}

func TestNormalizeAccessAuthorities_TrimsValues(t *testing.T) {
	meta := map[string]any{}
	_ = json.Unmarshal([]byte(`{"access_authorities":["  0xabc  ", "0xdef "]}`), &meta)
	vals, present, isNull := normalizeAccessAuthorities(meta)
	if !present || isNull {
		t.Fatal("expected present, non-null")
	}
	want := []string{"0xabc", "0xdef"}
	for i, v := range vals {
		if v != want[i] {
			t.Errorf("[%d] = %q, want %q", i, v, want[i])
		}
	}
}

func TestTrackCreate_AppliesAccessNormalization(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 900)
	tid := int64(TrackIDOffset + 9900)
	seedUser(t, pool, uid, "0xacc", "accu")

	meta := `{"owner_id":3000900,"title":"Access Track","genre":"Electronic","allowed_api_keys":["UPPER-KEY"],"access_authorities":["  0xLOWER  "]}`
	mustHandle(t, TrackCreate(),
		buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xacc", meta))

	var apiKeys []string
	var auth []string
	if err := pool.QueryRow(context.Background(),
		"SELECT allowed_api_keys, access_authorities FROM tracks WHERE track_id = $1 AND is_current = true",
		tid).Scan(&apiKeys, &auth); err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(apiKeys) != 1 || apiKeys[0] != "upper-key" {
		t.Errorf("allowed_api_keys = %v, want [upper-key]", apiKeys)
	}
	if len(auth) != 1 || auth[0] != "0xLOWER" {
		t.Errorf("access_authorities = %v, want [0xLOWER]", auth)
	}
}
