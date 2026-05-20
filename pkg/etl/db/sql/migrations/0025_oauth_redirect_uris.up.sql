-- oauth_redirect_uris: per-developer-app redirect URI list for OAuth.
-- Schema matches api/'s sql/01_schema.sql; IF NOT EXISTS makes this a
-- no-op against api/'s DB, while still creating the table for any
-- standalone consumer.

CREATE TABLE IF NOT EXISTS oauth_redirect_uris (
  id serial NOT NULL,
  client_id character varying(255) NOT NULL,
  redirect_uri text NOT NULL,
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT oauth_redirect_uris_pkey PRIMARY KEY (id)
);

CREATE INDEX IF NOT EXISTS ix_oauth_redirect_uris_client_id ON oauth_redirect_uris (client_id);
