-- Track downloads table matching discovery-provider schema.

CREATE TABLE IF NOT EXISTS track_downloads (
  txhash character varying NOT NULL,
  blocknumber integer REFERENCES blocks(number) ON DELETE CASCADE,
  parent_track_id integer NOT NULL,
  track_id integer NOT NULL,
  user_id integer NOT NULL,
  city character varying,
  region character varying,
  country character varying,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT track_downloads_pkey PRIMARY KEY (parent_track_id, track_id, txhash)
);

CREATE INDEX IF NOT EXISTS idx_track_downloads_user_id ON track_downloads (user_id);
CREATE INDEX IF NOT EXISTS idx_track_downloads_track_id ON track_downloads (track_id);
