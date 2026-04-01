-- Dashboard wallet user table matching discovery-provider schema.

CREATE TABLE IF NOT EXISTS dashboard_wallet_users (
  wallet character varying NOT NULL,
  user_id integer NOT NULL,
  is_delete boolean NOT NULL DEFAULT false,
  txhash character varying NOT NULL DEFAULT '',
  blockhash character varying NOT NULL DEFAULT '',
  blocknumber integer REFERENCES blocks(number) ON DELETE CASCADE,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT dashboard_wallet_users_pkey PRIMARY KEY (wallet)
);

CREATE INDEX IF NOT EXISTS idx_dashboard_wallet_users_user_id ON dashboard_wallet_users (user_id);
