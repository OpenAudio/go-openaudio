ALTER TABLE users
  DROP COLUMN IF EXISTS spl_usdc_payout_wallet,
  DROP COLUMN IF EXISTS coin_flair_mint,
  DROP COLUMN IF EXISTS profile_type;

DROP TYPE IF EXISTS profile_type_enum;
