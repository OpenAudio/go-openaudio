-- Three user profile fields the SDK accepts and clients send today, with
-- nowhere to store them.
--
-- UpdateProfileSchema carries spl_usdc_payout_wallet, coin_flair_mint and
-- profile_type, and the indexer had no column for any of them, so every write
-- was discarded. Measured on a production clone: 3,816 users have a payout
-- wallet, 472 a coin flair mint, 425 a profile type -- and in the 30 days
-- before the snapshot, 15, 47 and 24 users respectively set one. This is
-- ongoing loss, not a legacy gap.
--
-- profile_type mirrors the source's enum rather than using free text, so an
-- unrecognised value fails loudly instead of being stored and served.

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'profile_type_enum') THEN
    CREATE TYPE profile_type_enum AS ENUM ('label');
  END IF;
END
$$;

ALTER TABLE users
  ADD COLUMN IF NOT EXISTS spl_usdc_payout_wallet character varying,
  ADD COLUMN IF NOT EXISTS coin_flair_mint text,
  ADD COLUMN IF NOT EXISTS profile_type profile_type_enum;
