-- Shares table matching discovery-provider schema.

DO $$ BEGIN
  CREATE TYPE sharetype AS ENUM ('track', 'playlist', 'album');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS shares (
  blockhash character varying,
  blocknumber integer REFERENCES blocks(number) ON DELETE CASCADE,
  user_id integer NOT NULL,
  share_item_id integer NOT NULL,
  share_type sharetype NOT NULL,
  created_at timestamp without time zone NOT NULL,
  txhash character varying NOT NULL DEFAULT '',
  slot integer,
  CONSTRAINT shares_pkey PRIMARY KEY (user_id, share_item_id, share_type, txhash)
);

CREATE INDEX IF NOT EXISTS idx_shares_blocknumber ON shares (blocknumber);
CREATE INDEX IF NOT EXISTS idx_shares_user_id ON shares (user_id);
