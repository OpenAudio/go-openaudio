-- Associated wallets table matching discovery-provider schema.

CREATE TABLE IF NOT EXISTS associated_wallets (
  id serial NOT NULL,
  user_id integer NOT NULL,
  wallet character varying NOT NULL,
  chain character varying NOT NULL,
  is_current boolean NOT NULL DEFAULT true,
  is_delete boolean NOT NULL DEFAULT false,
  blockhash character varying NOT NULL DEFAULT '',
  blocknumber integer REFERENCES blocks(number) ON DELETE CASCADE,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT associated_wallets_pkey PRIMARY KEY (id)
);

CREATE INDEX IF NOT EXISTS ix_associated_wallets_user_id ON associated_wallets (user_id);
CREATE INDEX IF NOT EXISTS ix_associated_wallets_wallet ON associated_wallets (wallet);
