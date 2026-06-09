CREATE TABLE IF NOT EXISTS event_routes (
  slug          varchar  NOT NULL,
  title_slug    varchar  NOT NULL,
  collision_id  integer  NOT NULL,
  owner_id      integer  NOT NULL,
  event_id      integer  NOT NULL,
  is_current    boolean  NOT NULL,
  blockhash     varchar,
  blocknumber   integer,
  txhash        varchar,
  CONSTRAINT event_routes_pkey PRIMARY KEY (owner_id, slug)
);
CREATE INDEX IF NOT EXISTS event_routes_event_id_idx ON event_routes (event_id);
CREATE INDEX IF NOT EXISTS event_routes_is_current_idx ON event_routes (event_id, is_current);
