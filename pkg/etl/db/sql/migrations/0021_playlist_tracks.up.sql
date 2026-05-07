-- playlist_tracks: explicit junction table mirroring playlist_contents JSONB.
-- Schema from apps#0055_playlists_tracks.sql.
--
-- handle_playlist_track is intentionally not ported: apps' trigger only does
-- usdc_purchase notification fan-out (Solana, out of scope). Go-side
-- entity_manager handlers populate this table directly via updatePlaylistTracks.

CREATE TABLE IF NOT EXISTS playlist_tracks (
  playlist_id INTEGER NOT NULL,
  track_id INTEGER NOT NULL,
  is_removed BOOLEAN NOT NULL,
  created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (playlist_id, track_id)
);

CREATE INDEX IF NOT EXISTS idx_playlist_tracks_track_id ON playlist_tracks USING btree (track_id, created_at);
