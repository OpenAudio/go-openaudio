-- Tip-related tables matching discovery-provider schema.

CREATE TABLE IF NOT EXISTS user_tips (
  slot integer NOT NULL,
  signature character varying NOT NULL,
  sender_user_id integer NOT NULL,
  receiver_user_id integer NOT NULL,
  amount bigint NOT NULL,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT user_tips_pkey PRIMARY KEY (slot, signature)
);

CREATE INDEX IF NOT EXISTS ix_user_tips_sender_user_id ON user_tips (sender_user_id);
CREATE INDEX IF NOT EXISTS ix_user_tips_receiver_user_id ON user_tips (receiver_user_id);

CREATE TABLE IF NOT EXISTS reactions (
  id serial NOT NULL,
  reaction_value integer NOT NULL,
  sender_wallet character varying NOT NULL,
  reaction_type character varying NOT NULL,
  reacted_to character varying NOT NULL,
  timestamp timestamp without time zone NOT NULL,
  blocknumber integer NOT NULL REFERENCES blocks(number) ON DELETE CASCADE,
  CONSTRAINT reactions_pkey PRIMARY KEY (id)
);

CREATE INDEX IF NOT EXISTS ix_reactions_reacted_to_reaction_type ON reactions (reacted_to, reaction_type);
