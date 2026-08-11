package main

import (
	"bytes"
	"testing"
	"time"

	dbm "github.com/cometbft/cometbft-db"
	cmtapiversion "github.com/cometbft/cometbft/api/cometbft/version/v1"
	cmtstore "github.com/cometbft/cometbft/store"
	cmttypes "github.com/cometbft/cometbft/types"
	cmtversion "github.com/cometbft/cometbft/version"
)

// Resume reads the last block's commit back out of the blockstore, and which
// key it reads from is not interchangeable. Getting it wrong fails ONLY at the
// tail -- which is the exact and only case resume ever encounters.
//
// SaveBlock(block, parts, seenCommit) writes two different commits: seenCommit
// under the seen-commit key for block.Height, and block.LastCommit under the
// commit key for Height-1. LoadBlockCommit(h) reads the latter, so it resolves
// only once block h+1 has been saved. For the last block written there is no
// h+1, so it is permanently nil.
//
// The resulting failure is indistinguishable from data loss: "commit not found"
// against a blockstore that is completely intact. That is what it looked like
// when an interrupted five-hour run refused to resume.
func TestSeenCommitResolvesAtTheTailAndBlockCommitDoesNot(t *testing.T) {
	db, err := dbm.NewDB("blockstore", dbm.MemDBBackend, t.TempDir())
	if err != nil {
		t.Fatalf("open blockstore: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := cmtstore.NewBlockStore(db)

	block := &cmttypes.Block{
		Header: cmttypes.Header{
			Version:         cmtapiversion.Consensus{Block: cmtversion.BlockProtocol, App: 0},
			ChainID:         "test-chain",
			Height:          1,
			Time:            time.Unix(1, 0).UTC(),
			ProposerAddress: cmttypes.Address(bytes.Repeat([]byte{1}, 20)),
		},
		Data: cmttypes.Data{Txs: cmttypes.Txs{cmttypes.Tx("tx")}},
		// Height 1 has no predecessor, so an empty commit is valid here and
		// keeps the fixture to the one thing under test.
		LastCommit: &cmttypes.Commit{},
	}
	block.Header.LastCommitHash = block.LastCommit.Hash()
	block.Header.DataHash = block.Data.Hash()
	block.Header.EvidenceHash = block.Evidence.Hash()

	parts, err := block.MakePartSet(cmttypes.BlockPartSizeBytes)
	if err != nil {
		t.Fatalf("make part set: %v", err)
	}
	// The seen commit must reference the block it commits to; a bare Commit is
	// rejected as "commit cannot be for nil block".
	store.SaveBlock(block, parts, &cmttypes.Commit{
		Height:  1,
		BlockID: cmttypes.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()},
		Signatures: []cmttypes.CommitSig{{
			BlockIDFlag:      cmttypes.BlockIDFlagCommit,
			ValidatorAddress: block.Header.ProposerAddress,
			Timestamp:        block.Header.Time,
			Signature:        bytes.Repeat([]byte{2}, 64),
		}},
	})

	tail := store.Height()
	if tail != 1 {
		t.Fatalf("blockstore height = %d, want 1", tail)
	}
	if blk, _ := store.LoadBlock(tail); blk == nil {
		t.Fatal("LoadBlock at the tail is nil; nothing below this is meaningful")
	}

	if store.LoadSeenCommit(tail) == nil {
		t.Error("LoadSeenCommit is nil at the tail — resume cannot recover the last " +
			"commit, so an interrupted run could never be continued")
	}
	if store.LoadBlockCommit(tail) != nil {
		t.Error("LoadBlockCommit resolved at the tail. If CometBFT changed this, the " +
			"comment in writer.go explaining why resume uses LoadSeenCommit is now stale")
	}
}
