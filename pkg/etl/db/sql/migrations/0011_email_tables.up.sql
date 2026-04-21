-- Encrypted email tables matching discovery-provider schema.

CREATE TABLE IF NOT EXISTS encrypted_emails (
  email_owner_user_id integer NOT NULL,
  encrypted_email text NOT NULL,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT encrypted_emails_pkey PRIMARY KEY (email_owner_user_id)
);

CREATE TABLE IF NOT EXISTS email_access (
  email_owner_user_id integer NOT NULL,
  receiving_user_id integer NOT NULL,
  grantor_user_id integer NOT NULL,
  encrypted_key text NOT NULL,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT email_access_pkey PRIMARY KEY (email_owner_user_id, receiving_user_id, grantor_user_id)
);
