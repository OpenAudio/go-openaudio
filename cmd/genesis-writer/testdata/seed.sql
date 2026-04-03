-- Comprehensive seed data for genesis-writer integration tests.
-- Covers all entity types (users, tracks, playlists, social, plays) with
-- the full range of nullable column values so that genesis-writer's entity
-- serialization is exercised for every code path.
--
-- ID ranges mirror production offsets:
--   users:     >= 3,000,000
--   tracks:    >= 2,000,000
--   playlists: >= 400,000

-- ============================================================
-- BLOCK (anchor for all FK references)
-- ============================================================

INSERT INTO blocks (blockhash, number, parenthash, is_current) VALUES
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   '0x0000000000000000000000000000000000000000000000000000000000000000', true);

-- ============================================================
-- USERS
-- 3000001 – full artist profile: all optional text fields set, verified
-- 3000002 – artist with social handles but no bio/location
-- 3000003 – minimal artist: only wallet + handle, all nullable fields NULL
-- 3000004 – fan account: no artist data, website only
-- 3000005 – fan account: pure fan, all optional fields NULL
-- 3000006 – artist with cover_photo_sizes but no profile_picture fields
-- ============================================================

INSERT INTO users (
  blockhash, blocknumber, user_id, is_current, txhash,
  handle, handle_lc, wallet, name, bio, location,
  profile_picture, profile_picture_sizes,
  cover_photo, cover_photo_sizes,
  twitter_handle, instagram_handle, website,
  is_verified, is_deactivated, is_available, is_storage_v2,
  allow_ai_attribution, created_at, updated_at
) VALUES
  -- 1: full artist – every optional column filled
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    3000001, true, '0xaaa0000000000000000000000000000000000000000000000000000000000001',
    'full_artist', 'full_artist', '0xAAAA000000000000000000000000000000000001',
    'Full Artist Name',
    'Electronic producer based in LA with 10 years of experience crafting immersive soundscapes.',
    'Los Angeles, CA',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0001pic',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0001psz',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0001cov',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0001csz',
    'full_artist_tw', 'full_artist_ig', 'https://fullartist.example.com',
    true, false, true, true, false,
    '2021-01-01 00:00:00', '2021-01-01 00:00:00'
  ),
  -- 2: artist with social handles, no bio or location
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    3000002, true, '0xaaa0000000000000000000000000000000000000000000000000000000000002',
    'social_artist', 'social_artist', '0xAAAA000000000000000000000000000000000002',
    'Social Artist',
    NULL, NULL,
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0002pic',
    NULL,
    NULL,
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0002csz',
    'social_artist_tw', 'social_artist_ig', NULL,
    false, false, true, true, false,
    '2021-02-01 00:00:00', '2021-02-01 00:00:00'
  ),
  -- 3: minimal artist – every nullable field NULL
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    3000003, true, '0xaaa0000000000000000000000000000000000000000000000000000000000003',
    'minimal_artist', 'minimal_artist', '0xAAAA000000000000000000000000000000000003',
    NULL,
    NULL, NULL,
    NULL, NULL,
    NULL, NULL,
    NULL, NULL, NULL,
    false, false, true, false, false,
    '2021-03-01 00:00:00', '2021-03-01 00:00:00'
  ),
  -- 4: fan with website only
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    3000004, true, '0xaaa0000000000000000000000000000000000000000000000000000000000004',
    'fan_with_web', 'fan_with_web', '0xAAAA000000000000000000000000000000000004',
    'Fan With Website',
    NULL, NULL,
    NULL, NULL,
    NULL, NULL,
    NULL, NULL, 'https://fanwebsite.example.com',
    false, false, true, false, false,
    '2021-04-01 00:00:00', '2021-04-01 00:00:00'
  ),
  -- 5: pure fan – all optional text NULL
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    3000005, true, '0xaaa0000000000000000000000000000000000000000000000000000000000005',
    'pure_fan', 'pure_fan', '0xAAAA000000000000000000000000000000000005',
    'Pure Fan',
    NULL, NULL,
    NULL, NULL,
    NULL, NULL,
    NULL, NULL, NULL,
    false, false, true, false, false,
    '2021-05-01 00:00:00', '2021-05-01 00:00:00'
  ),
  -- 6: artist with cover_photo_sizes but no profile_picture
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    3000006, true, '0xaaa0000000000000000000000000000000000000000000000000000000000006',
    'cover_only_artist', 'cover_only_artist', '0xAAAA000000000000000000000000000000000006',
    'Cover Only Artist',
    'Has a cover photo but no profile picture.',
    'Nashville, TN',
    NULL, NULL,
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0006cov',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0006csz',
    NULL, NULL, NULL,
    false, false, true, true, false,
    '2021-06-01 00:00:00', '2021-06-01 00:00:00'
  );

-- ============================================================
-- TRACKS
-- 2000001 – full public track: bpm, musical_key, isrc, iswc, downloadable
-- 2000002 – unlisted track (is_unlisted=true)
-- 2000003 – stream gated (follow gate)
-- 2000004 – download gated
-- 2000005 – remix of 2000001
-- 2000006 – stem of 2000001
-- 2000007 – with release_date, preview_cid, is_original_available
-- 2000008 – jazz, all text fields filled
-- 2000009 – NULL title, genre, mood, tags (edge case for nullable strings)
-- ============================================================

INSERT INTO tracks (
  blockhash, blocknumber, track_id, is_current, is_delete, txhash,
  owner_id, title, description, duration, genre, mood, tags,
  track_cid, cover_art, cover_art_sizes, preview_cid,
  track_segments, is_downloadable, is_original_available,
  is_unlisted, is_scheduled_release, is_stream_gated, stream_conditions,
  is_download_gated, download_conditions,
  is_available, is_playlist_upload, is_owned_by_user,
  remix_of, stem_of,
  release_date,
  license, isrc, iswc, bpm, musical_key,
  comments_disabled, no_ai_use,
  created_at, updated_at
) VALUES
  -- 1: full public track with all metadata
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    2000001, true, false,
    '0xbbb0000000000000000000000000000000000000000000000000000000000001',
    3000001,
    'Sunrise Drive',
    'An energizing electronic track perfect for morning runs.',
    210, 'Electronic', 'Energizing', 'electronic,synth,morning',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0001cid',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0001cov',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0001csz',
    NULL,
    '[]', true, false,
    false, false, false, NULL,
    false, NULL,
    true, false, true,
    NULL, NULL,
    NULL,
    'CC BY 4.0', 'US-AB1-21-00001', 'T-123.456.789-Z',
    128.5, 'C major',
    false, false,
    '2021-06-01 00:00:00', '2021-06-01 00:00:00'
  ),
  -- 2: unlisted track
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    2000002, true, false,
    '0xbbb0000000000000000000000000000000000000000000000000000000000002',
    3000001,
    'Hidden Gem (Unlisted)',
    NULL,
    180, 'Electronic', 'Melancholic', NULL,
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0002cid',
    NULL, NULL, NULL,
    '[]', false, false,
    true, false, false, NULL,
    false, NULL,
    true, false, true,
    NULL, NULL,
    NULL,
    NULL, NULL, NULL,
    NULL, NULL,
    false, false,
    '2021-06-15 00:00:00', '2021-06-15 00:00:00'
  ),
  -- 3: stream gated (follow gate condition)
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    2000003, true, false,
    '0xbbb0000000000000000000000000000000000000000000000000000000000003',
    3000002,
    'Exclusive Track',
    'Only for followers.',
    240, 'Pop', 'Happy', 'pop,exclusive',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0003cid',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0003cov',
    NULL, NULL,
    '[]', false, false,
    false, false, true,
    '{"follow_user_id": 3000002}',
    false, NULL,
    true, false, true,
    NULL, NULL,
    NULL,
    NULL, NULL, NULL,
    NULL, NULL,
    false, false,
    '2021-07-01 00:00:00', '2021-07-01 00:00:00'
  ),
  -- 4: download gated
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    2000004, true, false,
    '0xbbb0000000000000000000000000000000000000000000000000000000000004',
    3000002,
    'Premium Download',
    'Download requires NFT ownership.',
    300, 'Hip-Hop/Rap', 'Excited', 'hiphop,premium',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0004cid',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0004cov',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0004csz',
    NULL,
    '[]', true, false,
    false, false, false, NULL,
    true,
    '{"nft_collection": {"chain": "eth", "address": "0xabcdef1234567890abcdef1234567890abcdef12", "standard": "ERC721", "name": "Test NFT"}}',
    true, false, true,
    NULL, NULL,
    NULL,
    NULL, NULL, NULL,
    NULL, NULL,
    false, false,
    '2021-07-15 00:00:00', '2021-07-15 00:00:00'
  ),
  -- 5: remix of track 2000001
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    2000005, true, false,
    '0xbbb0000000000000000000000000000000000000000000000000000000000005',
    3000006,
    'Sunrise Drive (Remix)',
    'A darker take on the original.',
    195, 'Electronic', 'Fiery', 'remix,electronic,dark',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0005cid',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0005cov',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0005csz',
    NULL,
    '[]', false, false,
    false, false, false, NULL,
    false, NULL,
    true, false, true,
    '{"tracks": [{"parent_track_id": 2000001}]}',
    NULL,
    NULL,
    NULL, NULL, NULL,
    140.0, 'A minor',
    false, false,
    '2021-08-01 00:00:00', '2021-08-01 00:00:00'
  ),
  -- 6: stem of track 2000001
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    2000006, true, false,
    '0xbbb0000000000000000000000000000000000000000000000000000000000006',
    3000001,
    'Sunrise Drive – Vocal Stem',
    NULL,
    210, NULL, NULL, NULL,
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0006cid',
    NULL, NULL, NULL,
    '[]', true, false,
    false, false, false, NULL,
    false, NULL,
    true, true, true,
    NULL,
    '{"parent_track_id": 2000001}',
    NULL,
    NULL, NULL, NULL,
    NULL, NULL,
    false, false,
    '2021-08-15 00:00:00', '2021-08-15 00:00:00'
  ),
  -- 7: with release_date, preview_cid, is_original_available
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    2000007, true, false,
    '0xbbb0000000000000000000000000000000000000000000000000000000000007',
    3000003,
    'Scheduled Release',
    'Coming soon.',
    270, 'Rock', 'Energizing', 'rock,upcoming',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0007cid',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0007cov',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0007csz',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0007prv',
    '[]', true, true,
    false, false, false, NULL,
    false, NULL,
    true, false, true,
    NULL, NULL,
    '2022-01-01 00:00:00',
    'All Rights Reserved',
    'GB-AB1-22-00007', NULL,
    95.0, 'D major',
    false, false,
    '2021-09-01 00:00:00', '2021-09-01 00:00:00'
  ),
  -- 8: jazz track, fully filled, original available
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    2000008, true, false,
    '0xbbb0000000000000000000000000000000000000000000000000000000000008',
    3000001,
    'Blue Note Sessions',
    'Recorded live in one take.',
    320, 'Jazz', 'Peaceful', 'jazz,live,acoustic',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0008cid',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0008cov',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0008csz',
    NULL,
    '[]', true, true,
    false, false, false, NULL,
    false, NULL,
    true, false, true,
    NULL, NULL,
    NULL,
    'CC BY-NC 4.0', NULL, NULL,
    NULL, 'Bb major',
    false, false,
    '2021-10-01 00:00:00', '2021-10-01 00:00:00'
  ),
  -- 9: null title, genre, mood, tags – edge case
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    2000009, true, false,
    '0xbbb0000000000000000000000000000000000000000000000000000000000009',
    3000003,
    NULL,
    NULL,
    120, NULL, NULL, NULL,
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0009cid',
    NULL, NULL, NULL,
    '[]', false, false,
    false, false, false, NULL,
    false, NULL,
    true, false, true,
    NULL, NULL,
    NULL,
    NULL, NULL, NULL,
    NULL, NULL,
    false, false,
    '2021-11-01 00:00:00', '2021-11-01 00:00:00'
  );

-- ============================================================
-- PLAYLISTS
-- 400001 – public album (is_album=true, is_private=false)
-- 400002 – private user playlist
-- 400003 – stream gated playlist
-- 400004 – album with release_date and description
-- ============================================================

INSERT INTO playlists (
  blockhash, blocknumber, playlist_id, is_current, is_delete, txhash,
  playlist_owner_id, playlist_name, is_album, is_private,
  is_scheduled_release, is_stream_gated, stream_conditions,
  is_image_autogenerated,
  playlist_contents, description,
  playlist_image_sizes_multihash,
  release_date,
  created_at, updated_at
) VALUES
  -- 1: public album
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    400001, true, false,
    '0xccc0000000000000000000000000000000000000000000000000000000000001',
    3000001, 'Sunrise EP', true, false,
    false, false, NULL,
    false,
    '{"track_ids": [{"track": 2000001, "timestamp": 1622505600}, {"track": 2000002, "timestamp": 1623715200}]}',
    'My debut EP featuring two electronic tracks.',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0401img',
    NULL,
    '2021-09-01 00:00:00', '2021-09-01 00:00:00'
  ),
  -- 2: private user playlist (no description, no image)
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    400002, true, false,
    '0xccc0000000000000000000000000000000000000000000000000000000000002',
    3000004, 'My Private Stash', false, true,
    false, false, NULL,
    false,
    '{"track_ids": [{"track": 2000001, "timestamp": 1622505600}, {"track": 2000008, "timestamp": 1633046400}]}',
    NULL,
    NULL,
    NULL,
    '2021-10-01 00:00:00', '2021-10-01 00:00:00'
  ),
  -- 3: stream gated playlist
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    400003, true, false,
    '0xccc0000000000000000000000000000000000000000000000000000000000003',
    3000002, 'Exclusive Playlist', false, false,
    false, true,
    '{"follow_user_id": 3000002}',
    false,
    '{"track_ids": [{"track": 2000003, "timestamp": 1625097600}, {"track": 2000004, "timestamp": 1626912000}]}',
    'Only for my followers.',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0403img',
    NULL,
    '2021-11-01 00:00:00', '2021-11-01 00:00:00'
  ),
  -- 4: album with release_date and description
  (
    '0x0000000000000000000000000000000000000000000000000000000000000001', 1,
    400004, true, false,
    '0xccc0000000000000000000000000000000000000000000000000000000000004',
    3000006, 'Future Sounds', true, false,
    false, false, NULL,
    false,
    '{"track_ids": [{"track": 2000005, "timestamp": 1628467200}, {"track": 2000007, "timestamp": 1630368000}]}',
    'A forward-looking album.',
    'baeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0404img',
    '2022-03-01 00:00:00',
    '2021-12-01 00:00:00', '2021-12-01 00:00:00'
  );

-- ============================================================
-- FOLLOWS
-- Various follower→followee pairs covering all 6 users
-- ============================================================

INSERT INTO follows (
  blockhash, blocknumber, follower_user_id, followee_user_id,
  is_current, is_delete, txhash, created_at
) VALUES
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000004, 3000001, true, false, '0xddd0000000000000000000000000000000000000000000000000000000000001', '2021-10-02 00:00:00'),
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000004, 3000002, true, false, '0xddd0000000000000000000000000000000000000000000000000000000000002', '2021-10-02 01:00:00'),
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000004, 3000006, true, false, '0xddd0000000000000000000000000000000000000000000000000000000000003', '2021-10-02 02:00:00'),
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000005, 3000001, true, false, '0xddd0000000000000000000000000000000000000000000000000000000000004', '2021-10-03 00:00:00'),
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000005, 3000002, true, false, '0xddd0000000000000000000000000000000000000000000000000000000000005', '2021-10-03 01:00:00'),
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000006, 3000001, true, false, '0xddd0000000000000000000000000000000000000000000000000000000000006', '2021-10-04 00:00:00'),
  -- mutual follow
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000001, 3000006, true, false, '0xddd0000000000000000000000000000000000000000000000000000000000007', '2021-10-04 01:00:00'),
  -- fan follows fan
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000004, 3000005, true, false, '0xddd0000000000000000000000000000000000000000000000000000000000008', '2021-10-05 00:00:00');

-- ============================================================
-- REPOSTS
-- track reposts and playlist reposts
-- ============================================================

INSERT INTO reposts (
  blockhash, blocknumber, user_id, repost_item_id, repost_type,
  is_current, is_delete, is_repost_of_repost, txhash, created_at
) VALUES
  -- track reposts
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000004, 2000001, 'track', true, false, false,
   '0xeee0000000000000000000000000000000000000000000000000000000000001', '2021-10-06 00:00:00'),
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000005, 2000001, 'track', true, false, false,
   '0xeee0000000000000000000000000000000000000000000000000000000000002', '2021-10-06 01:00:00'),
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000006, 2000008, 'track', true, false, false,
   '0xeee0000000000000000000000000000000000000000000000000000000000003', '2021-10-06 02:00:00'),
  -- playlist repost
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000005, 400002, 'playlist', true, false, false,
   '0xeee0000000000000000000000000000000000000000000000000000000000004', '2021-10-07 00:00:00'),
  -- album repost (stored as 'playlist' — 'album' was a historical type cleaned up by migration 0021)
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000004, 400001, 'playlist', true, false, false,
   '0xeee0000000000000000000000000000000000000000000000000000000000005', '2021-10-07 01:00:00');

-- ============================================================
-- SAVES
-- track saves and playlist saves
-- ============================================================

INSERT INTO saves (
  blockhash, blocknumber, user_id, save_item_id, save_type,
  is_current, is_delete, is_save_of_repost, txhash, created_at
) VALUES
  -- track saves
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000004, 2000001, 'track', true, false, false,
   '0xfff0000000000000000000000000000000000000000000000000000000000001', '2021-10-08 00:00:00'),
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000005, 2000008, 'track', true, false, false,
   '0xfff0000000000000000000000000000000000000000000000000000000000002', '2021-10-08 01:00:00'),
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000006, 2000001, 'track', true, false, false,
   '0xfff0000000000000000000000000000000000000000000000000000000000003', '2021-10-08 02:00:00'),
  -- playlist save
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000005, 400001, 'playlist', true, false, false,
   '0xfff0000000000000000000000000000000000000000000000000000000000004', '2021-10-09 00:00:00'),
  -- playlist save
  ('0x0000000000000000000000000000000000000000000000000000000000000001', 1,
   3000006, 400002, 'playlist', true, false, false,
   '0xfff0000000000000000000000000000000000000000000000000000000000005', '2021-10-09 01:00:00');

-- ============================================================
-- PLAYS
-- Mix of identified (user_id set) and anonymous (user_id=NULL)
-- Mix of full geo, partial geo, no geo
-- ============================================================

INSERT INTO plays (user_id, play_item_id, source, created_at, updated_at, signature, city, region, country) VALUES
  -- with user_id, full geo
  (3000004, 2000001, 'feed',    '2021-10-10 10:00:00', '2021-10-10 10:00:00', 'sig-0001', 'Los Angeles', 'CA', 'US'),
  -- with user_id, no geo
  (3000005, 2000001, 'search',  '2021-10-10 11:00:00', '2021-10-10 11:00:00', 'sig-0002', NULL, NULL, NULL),
  -- with user_id, partial geo (region + country only)
  (3000004, 2000008, 'profile', '2021-10-10 12:00:00', '2021-10-10 12:00:00', 'sig-0003', NULL, 'NY', 'US'),
  -- anonymous, city + country
  (NULL,    2000003, 'embed',   '2021-10-11 09:00:00', '2021-10-11 09:00:00', 'sig-0004', 'London', NULL, 'GB'),
  -- anonymous, no geo
  (NULL,    2000005, 'widget',  '2021-10-11 10:00:00', '2021-10-11 10:00:00', 'sig-0005', NULL, NULL, NULL),
  -- with user_id, country only
  (3000006, 2000008, 'trending','2021-10-12 14:00:00', '2021-10-12 14:00:00', 'sig-0006', NULL, NULL, 'DE'),
  -- same track played by multiple users (tests play aggregation)
  (3000003, 2000001, 'profile', '2021-10-13 08:00:00', '2021-10-13 08:00:00', 'sig-0007', NULL, NULL, NULL),
  (3000001, 2000001, 'trending','2021-10-13 09:00:00', '2021-10-13 09:00:00', 'sig-0008', 'New York', 'NY', 'US');
