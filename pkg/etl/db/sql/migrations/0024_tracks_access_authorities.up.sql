-- Add access_authorities to tracks (apps#13810 / apps#0177).

ALTER TABLE tracks ADD COLUMN IF NOT EXISTS access_authorities text[] DEFAULT NULL;
