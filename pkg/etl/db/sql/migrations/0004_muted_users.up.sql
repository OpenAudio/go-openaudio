-- muted_users table matching discovery-provider schema.

CREATE TABLE IF NOT EXISTS muted_users (
  muted_user_id integer NOT NULL,
  user_id integer NOT NULL,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  is_delete boolean DEFAULT false,
  txhash text NOT NULL,
  blockhash text NOT NULL DEFAULT '',
  blocknumber integer NOT NULL REFERENCES blocks(number) ON DELETE CASCADE,
  CONSTRAINT muted_users_pkey PRIMARY KEY (muted_user_id, user_id)
);
