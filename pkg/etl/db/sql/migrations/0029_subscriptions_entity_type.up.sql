ALTER TABLE subscriptions
  ADD COLUMN IF NOT EXISTS entity_type text NOT NULL DEFAULT 'User';

ALTER TABLE subscriptions
  ADD COLUMN IF NOT EXISTS entity_id integer;
