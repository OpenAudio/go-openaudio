-- Entity manager domain tables matching discovery-provider schema.
-- These tables are the target for entity manager validation and writes,
-- enabling the ETL indexer to replace the discovery-provider celery indexer.
--
-- PKs, FKs, and constraints match the production schema exactly
-- (see: AudiusProject/api sql/01_schema.sql) so this migration is
-- safe to run against an existing discovery-provider database via
-- CREATE TABLE IF NOT EXISTS.

-- Enums

DO $$ BEGIN
  CREATE TYPE savetype AS ENUM ('track', 'playlist', 'album');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
  CREATE TYPE reposttype AS ENUM ('track', 'playlist', 'album');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- blocks

CREATE TABLE IF NOT EXISTS blocks (
  blockhash character varying NOT NULL,
  parenthash character varying,
  is_current boolean,
  number integer,
  CONSTRAINT blocks_pkey PRIMARY KEY (blockhash),
  CONSTRAINT blocks_number_key UNIQUE (number)
);

CREATE UNIQUE INDEX IF NOT EXISTS blocks_is_current_idx ON blocks (is_current) WHERE is_current IS TRUE;

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
  blocknumber integer REFERENCES blocks(number) ON DELETE CASCADE,
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
  CONSTRAINT users_pkey PRIMARY KEY (txhash, user_id)
);

CREATE INDEX IF NOT EXISTS idx_users_blocknumber ON users (blocknumber);
CREATE INDEX IF NOT EXISTS idx_users_wallet ON users (wallet);
CREATE INDEX IF NOT EXISTS idx_users_handle_lc ON users (handle_lc);

-- tracks

CREATE TABLE IF NOT EXISTS tracks (
  blockhash character varying,
  track_id integer NOT NULL,
  is_current boolean NOT NULL,
  is_delete boolean NOT NULL,
  owner_id integer NOT NULL,
  title text,
  cover_art character varying,
  tags character varying,
  genre character varying,
  mood character varying,
  credits_splits character varying,
  create_date character varying,
  file_type character varying,
  metadata_multihash character varying,
  blocknumber integer REFERENCES blocks(number) ON DELETE CASCADE,
  created_at timestamp without time zone NOT NULL,
  description character varying,
  isrc character varying,
  iswc character varying,
  license character varying,
  updated_at timestamp without time zone NOT NULL,
  cover_art_sizes character varying,
  is_unlisted boolean NOT NULL DEFAULT false,
  field_visibility jsonb,
  route_id character varying,
  stem_of jsonb,
  remix_of jsonb,
  txhash character varying NOT NULL DEFAULT '',
  slot integer,
  is_available boolean NOT NULL DEFAULT true,
  is_stream_gated boolean NOT NULL DEFAULT false,
  stream_conditions jsonb,
  track_cid character varying,
  is_playlist_upload boolean NOT NULL DEFAULT false,
  duration integer DEFAULT 0,
  ai_attribution_user_id integer,
  preview_cid character varying,
  audio_upload_id character varying,
  preview_start_seconds double precision,
  release_date timestamp without time zone,
  track_segments jsonb NOT NULL DEFAULT '[]'::jsonb,
  is_scheduled_release boolean NOT NULL DEFAULT false,
  is_downloadable boolean NOT NULL DEFAULT false,
  is_download_gated boolean NOT NULL DEFAULT false,
  download_conditions jsonb,
  is_original_available boolean NOT NULL DEFAULT false,
  orig_file_cid character varying,
  orig_filename character varying,
  playlists_containing_track integer[] NOT NULL DEFAULT '{}'::integer[],
  placement_hosts text,
  ddex_app character varying,
  ddex_release_ids jsonb,
  artists jsonb,
  resource_contributors jsonb,
  indirect_resource_contributors jsonb,
  rights_controller jsonb,
  copyright_line jsonb,
  producer_copyright_line jsonb,
  parental_warning_type character varying,
  playlists_previously_containing_track jsonb NOT NULL DEFAULT jsonb_build_object(),
  allowed_api_keys text[],
  bpm double precision,
  musical_key character varying,
  audio_analysis_error_count integer NOT NULL DEFAULT 0,
  is_custom_bpm boolean DEFAULT false,
  is_custom_musical_key boolean DEFAULT false,
  comments_disabled boolean DEFAULT false,
  pinned_comment_id integer,
  cover_original_song_title character varying,
  cover_original_artist character varying,
  is_owned_by_user boolean NOT NULL DEFAULT false,
  no_ai_use boolean DEFAULT false,
  CONSTRAINT tracks_pkey PRIMARY KEY (txhash, track_id)
);

CREATE INDEX IF NOT EXISTS idx_tracks_blocknumber ON tracks (blocknumber);
CREATE INDEX IF NOT EXISTS idx_tracks_owner_id ON tracks (owner_id);
CREATE INDEX IF NOT EXISTS idx_tracks_created_at ON tracks (created_at);
CREATE INDEX IF NOT EXISTS idx_tracks_track_cid ON tracks (track_cid, is_delete);

-- playlists

CREATE TABLE IF NOT EXISTS playlists (
  blockhash character varying,
  blocknumber integer REFERENCES blocks(number) ON DELETE CASCADE,
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
  is_image_autogenerated boolean NOT NULL DEFAULT false,
  is_stream_gated boolean NOT NULL DEFAULT false,
  stream_conditions jsonb,
  ddex_app character varying,
  ddex_release_ids jsonb,
  artists jsonb,
  copyright_line jsonb,
  producer_copyright_line jsonb,
  parental_warning_type character varying,
  is_scheduled_release boolean NOT NULL DEFAULT false,
  release_date timestamp without time zone,
  CONSTRAINT playlists_pkey PRIMARY KEY (txhash, playlist_id)
);

CREATE INDEX IF NOT EXISTS idx_playlists_blocknumber ON playlists (blocknumber);
CREATE INDEX IF NOT EXISTS idx_playlists_playlist_owner_id ON playlists (playlist_owner_id);
CREATE INDEX IF NOT EXISTS idx_playlists_created_at ON playlists (created_at);

-- follows

CREATE TABLE IF NOT EXISTS follows (
  blockhash character varying,
  blocknumber integer REFERENCES blocks(number) ON DELETE CASCADE,
  follower_user_id integer NOT NULL,
  followee_user_id integer NOT NULL,
  is_current boolean NOT NULL,
  is_delete boolean NOT NULL,
  created_at timestamp without time zone NOT NULL,
  txhash character varying NOT NULL DEFAULT '',
  slot integer,
  CONSTRAINT follows_pkey PRIMARY KEY (followee_user_id, txhash, follower_user_id)
);

CREATE INDEX IF NOT EXISTS idx_follows_blocknumber ON follows (blocknumber);
CREATE INDEX IF NOT EXISTS idx_follows_follower_user_id ON follows (follower_user_id);
CREATE INDEX IF NOT EXISTS idx_follows_followee_user_id ON follows (followee_user_id);
CREATE INDEX IF NOT EXISTS follows_inbound_idx ON follows (followee_user_id, follower_user_id, is_delete);

-- saves

CREATE TABLE IF NOT EXISTS saves (
  blockhash character varying,
  blocknumber integer REFERENCES blocks(number) ON DELETE CASCADE,
  user_id integer NOT NULL,
  save_item_id integer NOT NULL,
  save_type savetype NOT NULL,
  is_current boolean NOT NULL,
  is_delete boolean NOT NULL,
  created_at timestamp without time zone NOT NULL,
  txhash character varying NOT NULL DEFAULT '',
  slot integer,
  is_save_of_repost boolean NOT NULL DEFAULT false,
  CONSTRAINT saves_pkey PRIMARY KEY (save_item_id, user_id, txhash, save_type)
);

CREATE INDEX IF NOT EXISTS idx_saves_blocknumber ON saves (blocknumber);
CREATE INDEX IF NOT EXISTS save_item_id_idx ON saves (save_item_id, save_type, user_id, is_delete);
CREATE INDEX IF NOT EXISTS save_user_id_idx ON saves (user_id, save_type, save_item_id, is_delete);

-- reposts

CREATE TABLE IF NOT EXISTS reposts (
  blockhash character varying,
  blocknumber integer REFERENCES blocks(number) ON DELETE CASCADE,
  user_id integer NOT NULL,
  repost_item_id integer NOT NULL,
  repost_type reposttype NOT NULL,
  is_current boolean NOT NULL,
  is_delete boolean NOT NULL,
  created_at timestamp without time zone NOT NULL,
  txhash character varying NOT NULL DEFAULT '',
  slot integer,
  is_repost_of_repost boolean NOT NULL DEFAULT false,
  CONSTRAINT reposts_pkey PRIMARY KEY (txhash, user_id, repost_item_id, repost_type)
);

CREATE INDEX IF NOT EXISTS idx_reposts_blocknumber ON reposts (blocknumber);
CREATE INDEX IF NOT EXISTS idx_reposts_created_at ON reposts (created_at);
CREATE INDEX IF NOT EXISTS repost_item_id_idx ON reposts (repost_item_id, repost_type, user_id, is_delete);
CREATE INDEX IF NOT EXISTS repost_user_id_idx ON reposts (user_id, repost_type, repost_item_id, created_at, is_delete);

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

CREATE INDEX IF NOT EXISTS track_routes_track_id_idx ON track_routes (track_id);

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

CREATE INDEX IF NOT EXISTS playlist_routes_playlist_id_idx ON playlist_routes (playlist_id);

-- developer_apps

CREATE TABLE IF NOT EXISTS developer_apps (
  address character varying NOT NULL,
  blockhash character varying,
  blocknumber integer,
  user_id integer,
  name character varying NOT NULL,
  is_personal_access boolean NOT NULL DEFAULT false,
  is_delete boolean NOT NULL DEFAULT false,
  created_at timestamp without time zone NOT NULL,
  txhash character varying NOT NULL,
  is_current boolean NOT NULL,
  updated_at timestamp without time zone NOT NULL,
  description character varying(255),
  image_url character varying,
  CONSTRAINT developer_apps_pkey PRIMARY KEY (txhash, address),
  CONSTRAINT unique_developer_apps_address UNIQUE (address)
);

-- grants

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
  CONSTRAINT grants_pkey PRIMARY KEY (user_id, txhash, grantee_address)
);

CREATE INDEX IF NOT EXISTS idx_grants_user_id ON grants (user_id);
