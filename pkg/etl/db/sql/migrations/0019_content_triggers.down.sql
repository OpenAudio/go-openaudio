DROP TRIGGER IF EXISTS on_playlist ON playlists;
DROP TRIGGER IF EXISTS on_track ON tracks;
DROP TRIGGER IF EXISTS on_user ON users;

DROP FUNCTION IF EXISTS handle_playlist();
DROP FUNCTION IF EXISTS handle_track();
DROP FUNCTION IF EXISTS handle_user();
DROP FUNCTION IF EXISTS track_should_notify(tracks, record, varchar);
DROP FUNCTION IF EXISTS track_is_public(record);

ALTER TABLE aggregate_user DROP COLUMN IF EXISTS total_track_count;
