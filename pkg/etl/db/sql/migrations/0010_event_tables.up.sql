-- Events table matching discovery-provider schema.

CREATE TABLE IF NOT EXISTS events (
  event_id integer NOT NULL,
  event_type character varying NOT NULL,
  user_id integer NOT NULL,
  entity_type character varying,
  entity_id integer,
  end_date timestamp without time zone,
  event_data jsonb,
  is_deleted boolean NOT NULL DEFAULT false,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  txhash character varying NOT NULL DEFAULT '',
  blockhash character varying NOT NULL DEFAULT '',
  blocknumber integer REFERENCES blocks(number) ON DELETE CASCADE,
  CONSTRAINT events_pkey PRIMARY KEY (event_id)
);

CREATE INDEX IF NOT EXISTS idx_events_entity_id ON events (entity_id);
CREATE INDEX IF NOT EXISTS idx_events_entity_type ON events (entity_type);
CREATE INDEX IF NOT EXISTS idx_events_created_at ON events (created_at);
