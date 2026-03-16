-- Entity manager domain tables matching discovery-provider schema.
-- These tables are the target for entity manager validation and writes,
-- enabling the ETL indexer to replace the discovery-provider celery indexer.

-- Enums

DO $$ BEGIN
  CREATE TYPE savetype AS ENUM ('track', 'playlist', 'album');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
  CREATE TYPE reposttype AS ENUM ('track', 'playlist', 'album');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- blocks (discovery-provider's block tracking table)

CREATE TABLE IF NOT EXISTS blocks (
  blockhash character varying NOT NULL,
  parenthash character varying,
  is_current boolean,
  number integer,
  CONSTRAINT blocks_pkey PRIMARY KEY (blockhash)
);

CREATE INDEX IF NOT EXISTS idx_blocks_number ON blocks (number);
CREATE INDEX IF NOT EXISTS idx_blocks_is_current ON blocks (is_current) WHERE is_current = true;

-- users

CREATE TABLE IF NOT EXISTS users (
  blockhash character varying,
  user_id integer NOT NULL,
  is_current boolean NOT NULL,
  handle character varying,
  wallet character varying,
  name text,
  profile_picture character varying,
  cover_photo character varying,
  bio character varying,
  location character varying,
  metadata_multihash character varying,
  creator_node_endpoint character varying,
  blocknumber integer REFERENCES blocks(number),
  is_verified boolean NOT NULL DEFAULT false,
  created_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp without time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
  handle_lc character varying,
  cover_photo_sizes character varying,
  profile_picture_sizes character varying,
  primary_id integer,
  secondary_ids integer[],
  replica_set_update_signer character varying,
  has_collectibles boolean NOT NULL DEFAULT false,
  txhash character varying NOT NULL DEFAULT '',
  playlist_library jsonb,
  is_deactivated boolean NOT NULL DEFAULT false,
  slot integer,
  user_storage_account character varying,
  user_authority_account character varying,
  artist_pick_track_id integer,
  is_available boolean NOT NULL DEFAULT true,
  is_storage_v2 boolean NOT NULL DEFAULT false,
  allow_ai_attribution boolean NOT NULL DEFAULT false,
  CONSTRAINT users_pkey PRIMARY KEY (is_current, user_id, txhash)
);

CREATE INDEX IF NOT EXISTS idx_users_blocknumber ON users (blocknumber);
CREATE INDEX IF NOT EXISTS idx_users_wallet ON users (wallet);
CREATE INDEX IF NOT EXISTS idx_users_handle_lc ON users (handle_lc);
CREATE INDEX IF NOT EXISTS idx_users_is_deactivated ON users (is_deactivated);
CREATE INDEX IF NOT EXISTS idx_users_user_id_is_current ON users (user_id) WHERE is_current = true;

-- tracks

CREATE TABLE IF NOT EXISTS tracks (
  blockhash character varying,
  track_id integer NOT NULL,
  is_current boolean NOT NULL,
  is_delete boolean NOT NULL,
  owner_id integer NOT NULL,
  title text,
  length integer,
  cover_art character varying,
  tags character varying,
  genre character varying,
  mood character varying,
  credits_splits character varying,
  create_date character varying,
  release_date character varying,
  file_type character varying,
  metadata_multihash character varying,
  blocknumber integer REFERENCES blocks(number),
  track_segments jsonb NOT NULL,
  created_at timestamp without time zone NOT NULL,
  description character varying,
  isrc character varying,
  iswc character varying,
  license character varying,
  updated_at timestamp without time zone NOT NULL,
  cover_art_sizes character varying,
  download jsonb,
  is_unlisted boolean NOT NULL DEFAULT false,
  field_visibility jsonb,
  route_id character varying,
  stem_of jsonb,
  remix_of jsonb,
  txhash character varying NOT NULL DEFAULT '',
  slot integer,
  is_available boolean NOT NULL DEFAULT true,
  is_premium boolean NOT NULL DEFAULT false,
  premium_conditions jsonb,
  track_cid character varying,
  is_playlist_upload boolean NOT NULL DEFAULT false,
  duration integer DEFAULT 0,
  ai_attribution_user_id integer,
  is_stream_gated boolean DEFAULT false,
  stream_conditions jsonb,
  is_download_gated boolean DEFAULT false,
  download_conditions jsonb,
  CONSTRAINT tracks_pkey PRIMARY KEY (is_current, track_id, txhash)
);

CREATE INDEX IF NOT EXISTS idx_tracks_blocknumber ON tracks (blocknumber);
CREATE INDEX IF NOT EXISTS idx_tracks_owner_id ON tracks (owner_id);
CREATE INDEX IF NOT EXISTS idx_tracks_created_at ON tracks (created_at);
CREATE INDEX IF NOT EXISTS idx_tracks_track_cid ON tracks (track_cid);
CREATE INDEX IF NOT EXISTS idx_tracks_track_id_is_current ON tracks (track_id) WHERE is_current = true;

-- playlists

CREATE TABLE IF NOT EXISTS playlists (
  blockhash character varying,
  blocknumber integer REFERENCES blocks(number),
  playlist_id integer NOT NULL,
  playlist_owner_id integer NOT NULL,
  is_album boolean NOT NULL,
  is_private boolean NOT NULL,
  playlist_name character varying,
  playlist_contents jsonb NOT NULL,
  playlist_image_multihash character varying,
  is_current boolean NOT NULL,
  is_delete boolean NOT NULL,
  description character varying,
  created_at timestamp without time zone NOT NULL,
  upc character varying,
  updated_at timestamp without time zone NOT NULL,
  playlist_image_sizes_multihash character varying,
  txhash character varying NOT NULL DEFAULT '',
  last_added_to timestamp without time zone,
  slot integer,
  metadata_multihash character varying,
  is_stream_gated boolean DEFAULT false,
  stream_conditions jsonb,
  CONSTRAINT playlists_pkey PRIMARY KEY (is_current, playlist_id, txhash)
);

CREATE INDEX IF NOT EXISTS idx_playlists_blocknumber ON playlists (blocknumber);
CREATE INDEX IF NOT EXISTS idx_playlists_playlist_owner_id ON playlists (playlist_owner_id);
CREATE INDEX IF NOT EXISTS idx_playlists_created_at ON playlists (created_at);
CREATE INDEX IF NOT EXISTS idx_playlists_playlist_id_is_current ON playlists (playlist_id) WHERE is_current = true;

-- follows

CREATE TABLE IF NOT EXISTS follows (
  blockhash character varying,
  blocknumber integer REFERENCES blocks(number),
  follower_user_id integer NOT NULL,
  followee_user_id integer NOT NULL,
  is_current boolean NOT NULL,
  is_delete boolean NOT NULL,
  created_at timestamp without time zone NOT NULL,
  txhash character varying NOT NULL DEFAULT '',
  slot integer,
  CONSTRAINT follows_pkey PRIMARY KEY (follower_user_id, followee_user_id, is_current, txhash)
);

CREATE INDEX IF NOT EXISTS idx_follows_blocknumber ON follows (blocknumber);
CREATE INDEX IF NOT EXISTS idx_follows_follower_user_id ON follows (follower_user_id);
CREATE INDEX IF NOT EXISTS idx_follows_followee_user_id ON follows (followee_user_id);
CREATE INDEX IF NOT EXISTS follows_inbound_idx ON follows (followee_user_id, follower_user_id, is_current, is_delete);

-- saves

CREATE TABLE IF NOT EXISTS saves (
  blockhash character varying,
  blocknumber integer REFERENCES blocks(number),
  user_id integer NOT NULL,
  save_item_id integer NOT NULL,
  save_type savetype NOT NULL,
  is_current boolean NOT NULL,
  is_delete boolean NOT NULL,
  created_at timestamp without time zone NOT NULL,
  txhash character varying NOT NULL DEFAULT '',
  slot integer,
  is_save_of_repost boolean NOT NULL DEFAULT false,
  CONSTRAINT saves_pkey PRIMARY KEY (user_id, save_item_id, save_type, is_current, txhash)
);

CREATE INDEX IF NOT EXISTS idx_saves_blocknumber ON saves (blocknumber);
CREATE INDEX IF NOT EXISTS save_item_id_idx ON saves (save_item_id, save_type);
CREATE INDEX IF NOT EXISTS save_user_id_idx ON saves (user_id, save_type);

-- reposts

CREATE TABLE IF NOT EXISTS reposts (
  blockhash character varying,
  blocknumber integer REFERENCES blocks(number),
  user_id integer NOT NULL,
  repost_item_id integer NOT NULL,
  repost_type reposttype NOT NULL,
  is_current boolean NOT NULL,
  is_delete boolean NOT NULL,
  created_at timestamp without time zone NOT NULL,
  txhash character varying NOT NULL DEFAULT '',
  slot integer,
  is_repost_of_repost boolean NOT NULL DEFAULT false,
  CONSTRAINT reposts_pkey PRIMARY KEY (user_id, repost_item_id, repost_type, is_current, txhash)
);

CREATE INDEX IF NOT EXISTS idx_reposts_blocknumber ON reposts (blocknumber);
CREATE INDEX IF NOT EXISTS idx_reposts_created_at ON reposts (created_at);
CREATE INDEX IF NOT EXISTS repost_item_id_idx ON reposts (repost_item_id, repost_type);
CREATE INDEX IF NOT EXISTS repost_user_id_idx ON reposts (user_id, repost_type);

-- track_routes

CREATE TABLE IF NOT EXISTS track_routes (
  slug character varying NOT NULL,
  title_slug character varying NOT NULL,
  collision_id integer NOT NULL,
  owner_id integer NOT NULL,
  track_id integer NOT NULL,
  is_current boolean NOT NULL,
  blockhash character varying NOT NULL,
  blocknumber integer NOT NULL,
  txhash character varying NOT NULL,
  CONSTRAINT track_routes_pkey PRIMARY KEY (owner_id, slug)
);

CREATE INDEX IF NOT EXISTS track_routes_track_id_idx ON track_routes (track_id, is_current);

-- playlist_routes

CREATE TABLE IF NOT EXISTS playlist_routes (
  slug character varying NOT NULL,
  title_slug character varying NOT NULL,
  collision_id integer NOT NULL,
  owner_id integer NOT NULL,
  playlist_id integer NOT NULL,
  is_current boolean NOT NULL,
  blockhash character varying NOT NULL,
  blocknumber integer NOT NULL,
  txhash character varying NOT NULL,
  CONSTRAINT playlist_routes_pkey PRIMARY KEY (owner_id, slug)
);

CREATE INDEX IF NOT EXISTS playlist_routes_playlist_id_idx ON playlist_routes (playlist_id, is_current);

-- developer_apps (renamed from app_delegates in discovery-provider)

CREATE TABLE IF NOT EXISTS developer_apps (
  address character varying NOT NULL,
  blockhash character varying,
  blocknumber integer,
  user_id integer,
  name character varying NOT NULL,
  description character varying(255),
  image_url character varying,
  is_personal_access boolean NOT NULL DEFAULT false,
  is_delete boolean NOT NULL DEFAULT false,
  created_at timestamp without time zone NOT NULL,
  txhash character varying NOT NULL,
  is_current boolean NOT NULL,
  updated_at timestamp without time zone NOT NULL,
  CONSTRAINT developer_apps_pkey PRIMARY KEY (address)
);

-- grants (renamed from delegations in discovery-provider)

CREATE TABLE IF NOT EXISTS grants (
  blockhash character varying,
  blocknumber integer,
  grantee_address character varying NOT NULL,
  user_id integer NOT NULL,
  is_revoked boolean NOT NULL DEFAULT false,
  is_current boolean NOT NULL,
  is_approved boolean,
  updated_at timestamp without time zone NOT NULL,
  created_at timestamp without time zone NOT NULL,
  txhash character varying NOT NULL,
  CONSTRAINT grants_pkey PRIMARY KEY (grantee_address, user_id)
);

CREATE INDEX IF NOT EXISTS idx_grants_user_id ON grants (user_id);
