-- Seed data required for the discovery provider indexer to start.
-- The indexer expects a block record to exist before it can process core events.
INSERT INTO public.blocks (blockhash, parenthash, number)
VALUES ('0x0000000000000000000000000000000000000000000000000000000000000000',
        '0x0000000000000000000000000000000000000000000000000000000000000000',
        0);
