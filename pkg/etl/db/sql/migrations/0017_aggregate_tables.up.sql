-- aggregate_user: rolled-up counts per user (followers, following, tracks, etc.)
-- Maintained by triggers on follows / saves / reposts / tracks / playlists.

CREATE TABLE IF NOT EXISTS aggregate_user (
  user_id integer NOT NULL,
  track_count bigint DEFAULT 0,
  playlist_count bigint DEFAULT 0,
  album_count bigint DEFAULT 0,
  follower_count bigint DEFAULT 0,
  following_count bigint DEFAULT 0,
  repost_count bigint DEFAULT 0,
  track_save_count bigint DEFAULT 0,
  supporter_count integer NOT NULL DEFAULT 0,
  supporting_count integer NOT NULL DEFAULT 0,
  CONSTRAINT aggregate_user_table_pkey PRIMARY KEY (user_id)
);

CREATE INDEX IF NOT EXISTS idx_aggregate_user_follower_count
  ON aggregate_user (user_id, follower_count);

-- aggregate_track: per-track repost / save counts.

CREATE TABLE IF NOT EXISTS aggregate_track (
  track_id integer NOT NULL,
  repost_count integer NOT NULL DEFAULT 0,
  save_count integer NOT NULL DEFAULT 0,
  CONSTRAINT aggregate_track_table_pkey PRIMARY KEY (track_id)
);

-- aggregate_playlist: per-playlist repost / save counts.

CREATE TABLE IF NOT EXISTS aggregate_playlist (
  playlist_id integer NOT NULL,
  is_album boolean,
  repost_count integer DEFAULT 0,
  save_count integer DEFAULT 0,
  CONSTRAINT aggregate_playlist_pkey PRIMARY KEY (playlist_id)
);

-- aggregate_plays: lifetime listen counter per track.

CREATE TABLE IF NOT EXISTS aggregate_plays (
  play_item_id integer NOT NULL,
  count bigint,
  CONSTRAINT play_item_id_pkey PRIMARY KEY (play_item_id)
);

-- aggregate_monthly_plays: monthly rollup of plays.

CREATE TABLE IF NOT EXISTS aggregate_monthly_plays (
  play_item_id integer NOT NULL,
  timestamp date NOT NULL DEFAULT CURRENT_TIMESTAMP,
  count integer NOT NULL,
  CONSTRAINT aggregate_monthly_plays_pkey PRIMARY KEY (play_item_id, timestamp)
);

-- milestones: notification thresholds (10/25/50/100/... follower-count etc.).

CREATE TABLE IF NOT EXISTS milestones (
  id integer NOT NULL,
  name character varying NOT NULL,
  threshold integer NOT NULL,
  blocknumber integer,
  slot integer,
  timestamp timestamp without time zone NOT NULL,
  CONSTRAINT milestones_pkey PRIMARY KEY (id, name, threshold)
);

CREATE INDEX IF NOT EXISTS milestones_name_idx ON milestones (name, id);
