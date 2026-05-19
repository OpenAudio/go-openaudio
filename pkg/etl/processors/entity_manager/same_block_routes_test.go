package entity_manager

import (
	"context"
	"testing"
)

// TestSameBlock_TrackRouteCollision asserts that two tracks with the same title
// from the same owner inserted "in the same block" (no transaction boundary
// between them) get distinct collision_ids on their track_routes rows.
//
// In apps this is handled by an explicit pending_track_routes list passed
// through block processing because Python defers the actual INSERT to the
// end of the block. In go-openaudio handlers Exec inserts immediately and
// the pool auto-commits, so the second handler's slug query already sees
// the first handler's row — same-block collisions resolve naturally.
//
// This test locks that behavior in: if a future refactor batches INSERTs
// into a single transaction (e.g. for performance), it will need an explicit
// pending_routes mechanism.
func TestSameBlock_TrackRouteCollision(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 600)
	t1 := int64(TrackIDOffset + 9000)
	t2 := int64(TrackIDOffset + 9001)
	seedUser(t, pool, uid, "0xsameblock", "sameblock")

	meta1 := `{"owner_id":3000600,"title":"My Song","genre":"Electronic"}`
	meta2 := `{"owner_id":3000600,"title":"My Song","genre":"Electronic"}`

	mustHandle(t, TrackCreate(),
		buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, t1, "0xsameblock", meta1))
	mustHandle(t, TrackCreate(),
		buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, t2, "0xsameblock", meta2))

	var slug1, slug2 string
	if err := pool.QueryRow(context.Background(),
		"SELECT slug FROM track_routes WHERE track_id = $1 AND is_current = true",
		t1).Scan(&slug1); err != nil {
		t.Fatalf("t1 slug: %v", err)
	}
	if err := pool.QueryRow(context.Background(),
		"SELECT slug FROM track_routes WHERE track_id = $1 AND is_current = true",
		t2).Scan(&slug2); err != nil {
		t.Fatalf("t2 slug: %v", err)
	}

	if slug1 == slug2 {
		t.Fatalf("expected distinct slugs for same-block same-title tracks, both got %q", slug1)
	}
	if slug1 != "my-song" {
		t.Errorf("first slug = %q, want my-song", slug1)
	}
	if slug2 != "my-song-1" {
		t.Errorf("second slug = %q, want my-song-1", slug2)
	}
}

func TestSameBlock_PlaylistRouteCollision(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 610)
	p1 := int64(PlaylistIDOffset + 9100)
	p2 := int64(PlaylistIDOffset + 9101)
	seedUser(t, pool, uid, "0xpsame", "psame")

	meta1 := `{"playlist_name":"Mix","is_album":false,"is_private":false}`
	meta2 := `{"playlist_name":"Mix","is_album":false,"is_private":false}`

	mustHandle(t, PlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, p1, "0xpsame", meta1))
	mustHandle(t, PlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, p2, "0xpsame", meta2))

	var slug1, slug2 string
	if err := pool.QueryRow(context.Background(),
		"SELECT slug FROM playlist_routes WHERE playlist_id = $1 AND is_current = true",
		p1).Scan(&slug1); err != nil {
		t.Fatalf("p1 slug: %v", err)
	}
	if err := pool.QueryRow(context.Background(),
		"SELECT slug FROM playlist_routes WHERE playlist_id = $1 AND is_current = true",
		p2).Scan(&slug2); err != nil {
		t.Fatalf("p2 slug: %v", err)
	}

	if slug1 == slug2 {
		t.Fatalf("expected distinct slugs, both got %q", slug1)
	}
}
