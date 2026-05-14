-- stems

CREATE TABLE IF NOT EXISTS stems (
  parent_track_id integer NOT NULL,
  child_track_id integer NOT NULL,
  CONSTRAINT stems_pkey PRIMARY KEY (parent_track_id, child_track_id)
);

-- remixes

CREATE TABLE IF NOT EXISTS remixes (
  parent_track_id integer NOT NULL,
  child_track_id integer NOT NULL,
  CONSTRAINT remixes_pkey PRIMARY KEY (parent_track_id, child_track_id)
);

CREATE INDEX IF NOT EXISTS remixes_child_idx ON remixes (child_track_id, parent_track_id);
