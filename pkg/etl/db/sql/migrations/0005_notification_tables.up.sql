-- Notification tables.

CREATE TABLE IF NOT EXISTS notification (
  id serial NOT NULL,
  specifier character varying NOT NULL,
  group_id character varying NOT NULL,
  type character varying NOT NULL,
  slot integer,
  blocknumber integer,
  timestamp timestamp without time zone NOT NULL,
  data jsonb,
  user_ids integer[],
  type_v2 character varying,
  CONSTRAINT notification_pkey PRIMARY KEY (id),
  CONSTRAINT uq_notification UNIQUE (group_id, specifier)
);

CREATE INDEX IF NOT EXISTS ix_notification ON notification USING gin (user_ids);

CREATE TABLE IF NOT EXISTS notification_seen (
  user_id integer NOT NULL,
  seen_at timestamp without time zone NOT NULL,
  blocknumber integer,
  blockhash character varying,
  txhash character varying,
  CONSTRAINT notification_seen_pkey PRIMARY KEY (user_id, seen_at)
);

-- playlist_seen PK matches prod's schema: (user_id, playlist_id, seen_at).
-- The `is_current` column is retained for backward compatibility with the
-- legacy schema (always written as `true` by the playlist-seen handler), but
-- is NOT part of the uniqueness contract — including it in the PK would
-- mismatch prod and cause 42P10 on any ON CONFLICT clause that names the
-- prod target columns. Older databases that were created with the wrong PK
-- get fixed by migration 0027_fix_playlist_seen_pkey.up.sql.
CREATE TABLE IF NOT EXISTS playlist_seen (
  is_current boolean NOT NULL,
  user_id integer NOT NULL,
  playlist_id integer NOT NULL,
  seen_at timestamp without time zone NOT NULL,
  blocknumber integer,
  blockhash character varying,
  txhash character varying,
  CONSTRAINT playlist_seen_pkey PRIMARY KEY (user_id, playlist_id, seen_at)
);
