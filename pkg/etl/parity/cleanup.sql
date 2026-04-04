-- Cleanup script: removes all Go ETL data after a given boundary.
-- Usage: psql "$ETL_DB_URL" -v em_block=116630234 -v chain_height=22275838 -f cleanup.sql

-- Domain tables (blocknumber = em_block number)
DELETE FROM comment_mentions WHERE blocknumber > :em_block;
DELETE FROM comment_reactions WHERE blocknumber > :em_block;
DELETE FROM comment_reports WHERE blocknumber > :em_block;
DELETE FROM comments WHERE blocknumber > :em_block;
DELETE FROM follows WHERE blocknumber > :em_block;
DELETE FROM saves WHERE blocknumber > :em_block;
DELETE FROM reposts WHERE blocknumber > :em_block;
DELETE FROM subscriptions WHERE blocknumber > :em_block;
DELETE FROM shares WHERE blocknumber > :em_block;
DELETE FROM track_downloads WHERE blocknumber > :em_block;
DELETE FROM grants WHERE blocknumber > :em_block;
DELETE FROM developer_apps WHERE blocknumber > :em_block;
DELETE FROM muted_users WHERE blocknumber > :em_block;
DELETE FROM associated_wallets WHERE blocknumber > :em_block;
DELETE FROM dashboard_wallet_users WHERE blocknumber > :em_block;
DELETE FROM events WHERE blocknumber > :em_block;
DELETE FROM reactions WHERE blocknumber > :em_block;
DELETE FROM notification WHERE blocknumber > :em_block;
DELETE FROM tracks WHERE blocknumber > :em_block;
DELETE FROM playlists WHERE blocknumber > :em_block;
DELETE FROM users WHERE blocknumber > :em_block;

-- ETL tables (block_height = chain height)
DELETE FROM etl_blocks WHERE block_height > :chain_height;
DELETE FROM etl_transactions WHERE block_height > :chain_height;
DELETE FROM etl_manage_entities WHERE block_height > :chain_height;
DELETE FROM etl_addresses WHERE first_seen_block_height > :chain_height;

-- Block tracking (keep at least one core_indexed_blocks row so em_block offset survives)
DELETE FROM blocks WHERE number > :em_block;
DELETE FROM core_indexed_blocks WHERE height > :chain_height;
