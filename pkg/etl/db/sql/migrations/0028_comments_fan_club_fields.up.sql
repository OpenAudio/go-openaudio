-- Add fan-club text-post fields to comments.
--
-- is_members_only gates a FanClub text post to paying members; only honored
-- when entity_type='FanClub' (validation enforced in comment_create.go).
-- video_url stores an attached video URL on a text post (apps#14080).
--
-- Mirrors apps' models/social/comment.py and entity_manager/entities/comment.py
-- additions from PRs #14029, #14054, #14058, #14080.

ALTER TABLE comments
  ADD COLUMN IF NOT EXISTS is_members_only boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS video_url text;
