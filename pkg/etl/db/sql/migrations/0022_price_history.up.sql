-- track_price_history + album_price_history: USDC gating audit trail.
-- Combined schema from apps#0017_track_price_history, #0051_usdc_purchase_access,
-- and #0058_album_price_history.

DO $$ BEGIN
  CREATE TYPE usdc_purchase_access_type AS ENUM ('stream', 'download');
EXCEPTION
  WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS track_price_history (
  track_id integer NOT NULL,
  splits jsonb NOT NULL,
  total_price_cents bigint NOT NULL,
  blocknumber integer NOT NULL,
  block_timestamp timestamp WITHOUT TIME ZONE NOT NULL,
  created_at timestamp WITHOUT TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
  access usdc_purchase_access_type NOT NULL DEFAULT 'stream',
  CONSTRAINT track_price_history_pkey PRIMARY KEY (track_id, block_timestamp)
);

CREATE TABLE IF NOT EXISTS album_price_history (
  playlist_id integer NOT NULL,
  splits jsonb NOT NULL,
  total_price_cents bigint NOT NULL,
  blocknumber integer NOT NULL,
  block_timestamp timestamp WITHOUT TIME ZONE NOT NULL,
  created_at timestamp WITHOUT TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT album_price_history_pkey PRIMARY KEY (playlist_id, block_timestamp)
);
