-- Subscriptions table matching discovery-provider schema.

CREATE TABLE IF NOT EXISTS subscriptions (
  blockhash character varying,
  blocknumber integer REFERENCES blocks(number) ON DELETE CASCADE,
  subscriber_id integer NOT NULL,
  user_id integer NOT NULL,
  is_current boolean NOT NULL,
  is_delete boolean NOT NULL,
  created_at timestamp without time zone NOT NULL,
  txhash character varying NOT NULL DEFAULT '',
  CONSTRAINT subscriptions_pkey PRIMARY KEY (subscriber_id, user_id, txhash)
);

CREATE INDEX IF NOT EXISTS ix_subscriptions_blocknumber ON subscriptions (blocknumber);
CREATE INDEX IF NOT EXISTS ix_subscriptions_user_id ON subscriptions (user_id);
