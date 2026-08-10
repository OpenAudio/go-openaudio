package entity_manager

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
)

// removedPlaylistTrack is a track that used to be in a playlist and is not in
// it any more — a playlist_tracks row with is_removed = true. Because the
// junction table's primary key is (playlist_id, track_id), a removal flips the
// flag rather than deleting the row, so the row is a tombstone: the record that
// the track was once a member, and when it left.
//
// CreatedAt is when the track joined; UpdatedAt is when it left.
type removedPlaylistTrack struct {
	TrackID   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// insertPlaylistTrackTombstones writes the removal history a migrated playlist
// carries on its Create.
//
// The tombstones cannot be derived the way production derives them.
// updatePlaylistTracks recognizes a removal as a transition — a track that was
// in playlist_contents before an update and is absent after it — and the
// migration replays a snapshot: one Playlist/Create holding the playlist's
// final contents, with no earlier state to differ from. The "was present, now
// absent" loop has nothing to iterate, so a Create alone can only ever produce
// live rows. On a production clone that is 20,763 tombstones across 3,917
// playlists that would simply not exist in the migrated data.
//
// They are not bookkeeping. They are the input to
// tracks.playlists_previously_containing_track, which the API reads to decide
// whether someone who bought an album still has access to a track that later
// left it — 19,441 tracks in the source have such a record. So each tombstone
// updates the reverse index here too, through the same function the production
// removal path uses, and carries the source's own timestamps: the removal time
// is compared against a purchase date, so it has to be when the track actually
// left, not when the migration replayed it.
//
// currentTrackIDs is the playlist's live membership. A track appearing in both
// is a contradiction in the source; the live row wins and the tombstone is
// dropped, so a replay can never mark a present track removed.
func insertPlaylistTrackTombstones(
	ctx context.Context,
	dbtx db.DBTX,
	playlistID int64,
	removals []removedPlaylistTrack,
	currentTrackIDs []int64,
	blockTime time.Time,
) error {
	if len(removals) == 0 {
		return nil
	}

	seen := make(map[int64]struct{}, len(currentTrackIDs)+len(removals))
	for _, id := range currentTrackIDs {
		seen[id] = struct{}{}
	}

	pending := make([]removedPlaylistTrack, 0, len(removals))
	for _, r := range removals {
		if _, dup := seen[r.TrackID]; dup {
			continue
		}
		seen[r.TrackID] = struct{}{}
		if r.UpdatedAt.IsZero() {
			r.UpdatedAt = blockTime
		}
		if r.CreatedAt.IsZero() {
			r.CreatedAt = r.UpdatedAt
		}
		pending = append(pending, r)
	}
	if len(pending) == 0 {
		return nil
	}

	// Sorted by removal time so the reverse-index statements below batch into
	// runs, and so a replay of the same input issues the same statements.
	slices.SortFunc(pending, func(a, b removedPlaylistTrack) int {
		if c := a.UpdatedAt.Compare(b.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.TrackID, b.TrackID)
	})

	for _, r := range pending {
		if _, err := dbtx.Exec(ctx, `
			INSERT INTO playlist_tracks (playlist_id, track_id, is_removed, created_at, updated_at)
			VALUES ($1, $2, true, $3, $4)
			ON CONFLICT (playlist_id, track_id) DO NOTHING
		`, playlistID, r.TrackID, r.CreatedAt, r.UpdatedAt); err != nil {
			return err
		}
	}

	// One reverse-index write per distinct removal time: the statement stamps a
	// single timestamp across every track it touches, so tracks that left at
	// different moments cannot share one.
	for start := 0; start < len(pending); {
		end := start + 1
		for end < len(pending) && pending[end].UpdatedAt.Equal(pending[start].UpdatedAt) {
			end++
		}
		ids := make([]int64, 0, end-start)
		for _, r := range pending[start:end] {
			ids = append(ids, r.TrackID)
		}
		if err := updateTrackPlaylistIndex(ctx, dbtx, playlistID, nil, ids, pending[start].UpdatedAt); err != nil {
			return err
		}
		start = end
	}

	return nil
}
