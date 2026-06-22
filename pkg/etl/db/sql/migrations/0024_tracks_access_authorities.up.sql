-- Add access_authorities column to tracks.

ALTER TABLE tracks ADD COLUMN IF NOT EXISTS access_authorities text[] DEFAULT NULL;
