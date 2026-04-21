-- Comment tables matching discovery-provider schema.

CREATE TABLE IF NOT EXISTS comments (
  comment_id integer NOT NULL,
  text text NOT NULL,
  user_id integer NOT NULL,
  entity_id integer NOT NULL,
  entity_type text NOT NULL,
  track_timestamp_s integer,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  is_delete boolean DEFAULT false,
  is_visible boolean DEFAULT true,
  is_edited boolean DEFAULT false,
  txhash text NOT NULL,
  blockhash text NOT NULL DEFAULT '',
  blocknumber integer NOT NULL REFERENCES blocks(number) ON DELETE CASCADE,
  CONSTRAINT comments_pkey PRIMARY KEY (comment_id)
);

CREATE TABLE IF NOT EXISTS comment_reactions (
  comment_id integer NOT NULL,
  user_id integer NOT NULL,
  created_at timestamp without time zone,
  updated_at timestamp without time zone,
  is_delete boolean NOT NULL DEFAULT false,
  txhash text NOT NULL,
  blockhash text NOT NULL DEFAULT '',
  blocknumber integer NOT NULL REFERENCES blocks(number) ON DELETE CASCADE,
  CONSTRAINT comment_reactions_pkey PRIMARY KEY (comment_id, user_id)
);

CREATE TABLE IF NOT EXISTS comment_reports (
  comment_id integer NOT NULL,
  user_id integer NOT NULL,
  created_at timestamp without time zone,
  updated_at timestamp without time zone,
  is_delete boolean NOT NULL DEFAULT false,
  txhash text NOT NULL,
  blockhash text NOT NULL DEFAULT '',
  blocknumber integer NOT NULL REFERENCES blocks(number) ON DELETE CASCADE,
  CONSTRAINT comment_reports_pkey PRIMARY KEY (comment_id, user_id)
);

CREATE TABLE IF NOT EXISTS comment_mentions (
  comment_id integer NOT NULL,
  user_id integer NOT NULL,
  created_at timestamp without time zone,
  updated_at timestamp without time zone,
  is_delete boolean NOT NULL DEFAULT false,
  txhash text NOT NULL,
  blockhash text NOT NULL DEFAULT '',
  blocknumber integer NOT NULL REFERENCES blocks(number) ON DELETE CASCADE,
  CONSTRAINT comment_mentions_pkey PRIMARY KEY (comment_id, user_id)
);

CREATE TABLE IF NOT EXISTS comment_threads (
  parent_comment_id integer NOT NULL,
  comment_id integer NOT NULL,
  CONSTRAINT comment_threads_pkey PRIMARY KEY (parent_comment_id, comment_id)
);

CREATE TABLE IF NOT EXISTS comment_notification_settings (
  user_id integer NOT NULL,
  entity_id integer NOT NULL,
  entity_type text NOT NULL,
  is_muted boolean DEFAULT false,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT comment_notification_settings_pkey PRIMARY KEY (user_id, entity_id, entity_type)
);
