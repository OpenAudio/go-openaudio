package entity_manager

import (
	"context"
	"testing"
)

func TestCreateTrackRouteID(t *testing.T) {
	tests := []struct {
		title  string
		handle string
		want   string
	}{
		{"My Awesome Track!", "Alice", "alice/my-awesome-track"},
		{"Bonjour, comment ça va?", "BoB", "bob/bonjour-comment-ça-va"},
		{"  spaced   out ", "carol", "carol/-spaced-out-"},
	}
	for _, tt := range tests {
		got := CreateTrackRouteID(tt.title, tt.handle)
		if got != tt.want {
			t.Errorf("CreateTrackRouteID(%q, %q) = %q, want %q", tt.title, tt.handle, got, tt.want)
		}
	}
}

func TestTrackCreate_SetsRouteID(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 120)
	tid := int64(TrackIDOffset + 400)

	seedUser(t, pool, uid, "0xroute", "rayhandle")
	meta := `{"owner_id":3000120,"title":"Cool Title!","genre":"Electronic"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xroute", meta)
	mustHandle(t, TrackCreate(), params)

	var routeID *string
	if err := pool.QueryRow(context.Background(),
		"SELECT route_id FROM tracks WHERE track_id = $1 AND is_current = true", tid).Scan(&routeID); err != nil {
		t.Fatalf("query route_id: %v", err)
	}
	if routeID == nil || *routeID != "rayhandle/cool-title" {
		t.Fatalf("route_id = %v, want 'rayhandle/cool-title'", routeID)
	}
}

func TestTrackCreate_GuestUserSyntheticHandle(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 130)
	tid := int64(TrackIDOffset + 500)

	// Seed a user with empty handle to simulate a guest
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO users (user_id, handle, handle_lc, wallet, is_current, is_verified, is_deactivated, is_available, created_at, updated_at, txhash)
		VALUES ($1, NULL, NULL, $2, true, false, false, true, now(), now(), '')
		ON CONFLICT DO NOTHING
	`, uid, "0xguest"); err != nil {
		t.Fatalf("seed guest: %v", err)
	}

	meta := `{"owner_id":3000130,"title":"Guest Track","genre":"Electronic"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xguest", meta)
	mustHandle(t, TrackCreate(), params)

	var routeID *string
	if err := pool.QueryRow(context.Background(),
		"SELECT route_id FROM tracks WHERE track_id = $1 AND is_current = true", tid).Scan(&routeID); err != nil {
		t.Fatalf("query route_id: %v", err)
	}
	wantPrefix := "user-3000130/"
	if routeID == nil || len(*routeID) < len(wantPrefix) || (*routeID)[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("route_id = %v, want prefix %q", routeID, wantPrefix)
	}
}

func TestTrackUpdate_RebuildsRouteIDOnTitleChange(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 150)
	tid := int64(TrackIDOffset + 700)

	seedUser(t, pool, uid, "0xtitle", "titler")
	seedTrackFull(t, pool, tid, uid, "Original Title")

	meta := `{"title":"Brand New Title"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xtitle", meta)
	mustHandle(t, TrackUpdate(), params)

	var routeID *string
	if err := pool.QueryRow(context.Background(),
		"SELECT route_id FROM tracks WHERE track_id = $1 AND is_current = true", tid).Scan(&routeID); err != nil {
		t.Fatalf("query route_id: %v", err)
	}
	if routeID == nil || *routeID != "titler/brand-new-title" {
		t.Fatalf("route_id = %v, want 'titler/brand-new-title'", routeID)
	}
}

func TestPlaylistCreate_InsertsLegacyIDSuffixedRoute(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 170)
	pid := int64(PlaylistIDOffset + 900)

	seedUser(t, pool, uid, "0xplaylistmigr", "plmigr")
	meta := `{"playlist_name":"Migration Album","is_album":true}`
	params := buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xplaylistmigr", meta)
	mustHandle(t, PlaylistCreate(), params)

	var current, total int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM playlist_routes WHERE playlist_id = $1 AND is_current = true", pid).Scan(&current); err != nil {
		t.Fatalf("count current routes: %v", err)
	}
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM playlist_routes WHERE playlist_id = $1", pid).Scan(&total); err != nil {
		t.Fatalf("count routes: %v", err)
	}
	if current != 1 {
		t.Errorf("expected exactly 1 current route, got %d", current)
	}
	if total != 2 {
		t.Errorf("expected 2 total playlist_routes (current + legacy ID-suffixed), got %d", total)
	}
}
