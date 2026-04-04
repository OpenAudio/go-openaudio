CREATE TABLE IF NOT EXISTS core_indexed_blocks (
  blockhash character varying NOT NULL,
  parenthash character varying,
  chain_id text NOT NULL,
  height integer NOT NULL,
  plays_slot integer DEFAULT 0,
  em_block integer,
  CONSTRAINT pk_chain_id_height PRIMARY KEY (chain_id, height)
);

CREATE INDEX IF NOT EXISTS idx_chain_blockhash ON core_indexed_blocks (blockhash);
CREATE INDEX IF NOT EXISTS idx_chain_id_height ON core_indexed_blocks (chain_id, height);
